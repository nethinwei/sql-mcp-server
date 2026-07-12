// The workload track (-track workload) runs the fixtures/v4 realistic
// business workload (docs/design/business-workload-model.md): complex
// multi-module tasks graded on two channels — a mechanical answer check
// whose key values come from the fixture generator (single source of
// truth), and an evidence channel that scans tool results for the expected
// result rows and attributes failures to one of the roadmap's ten failure
// categories.
//
// Additional environment (dogfooding mode):
//
//	EVAL_DSN     optional; external database DSN — skips the testcontainer
//	             and fixture seeding (bring your own data)
//	EVAL_CONFIG  optional; server config path (default the v4 profile)
//	EVAL_TASKS   optional; task file path (default fixtures/v4/tasks/tasks.yaml)
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"gopkg.in/yaml.v3"

	workload "github.com/nethinwei/sql-mcp-server/fixtures/v4/generator"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
	pgprov "github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

// Workload-track cost caps: fewer tasks than the regression track but each
// may legitimately need more calls (multi-entity decomposition). Growing
// past these requires an explicit roadmap decision.
const (
	maxWorkloadTasks     = 40
	maxWorkloadToolCalls = 16
	workloadTokenLimit   = 2_000_000
)

// failureCategories are the ten attribution outlets fixed by the roadmap
// (v0.1.9 stage result). Task files may only reference these.
var failureCategories = map[string]bool{
	"agent_discovery":       true, // wrong or no entity found
	"argument_construction": true, // invalid tool arguments
	"relation_selection":    true, // wrong join/expansion path
	"grain":                 true, // wrong aggregation grain
	"time_semantics":        true, // wrong time field or range
	"status_semantics":      true, // wrong status filter or mapping
	"unit_currency":         true, // minor units, scale, or currency mixed up
	"ir_expressibility":     true, // not expressible in bounded governed calls
	"provider_divergence":   true, // provider-specific behavior (conformance suite's job)
	"governance_policy":     true, // blocked by policy/cost/budget
}

// workloadTask is one v4 task. Expected answers are never written in the
// task file: the loader injects them from the fixture generator so fixture
// and grading cannot drift apart.
type workloadTask struct {
	ID                string   `yaml:"id"`
	Module            string   `yaml:"module"`
	Prompt            string   `yaml:"prompt"`
	ExpectTool        string   `yaml:"expect_tool"`
	Capabilities      []string `yaml:"capabilities"`
	SemanticTraps     []string `yaml:"semantic_traps"`
	FailureCategories []string `yaml:"failure_categories"`

	answerContains []string   // injected: key values the answer must contain
	expectedRows   [][]string // injected: expected result rows (evidence channel)
}

type workloadFile struct {
	Version      int            `yaml:"version"`
	MaxToolCalls int            `yaml:"max_tool_calls"`
	Tasks        []workloadTask `yaml:"tasks"`
}

// loadWorkloadTasks reads the task file and injects the generator-computed
// expectations. Fail closed: a task without a computed expectation or with
// an unknown failure category is a configuration error.
func loadWorkloadTasks(path string, expectations map[string]workload.Expectation) (workloadFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return workloadFile{}, err
	}
	var file workloadFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return workloadFile{}, err
	}
	if len(file.Tasks) == 0 {
		return workloadFile{}, fmt.Errorf("no tasks in %s", path)
	}
	if len(file.Tasks) > maxWorkloadTasks {
		return workloadFile{}, fmt.Errorf("%d tasks exceed the hard cap of %d", len(file.Tasks), maxWorkloadTasks)
	}
	if file.MaxToolCalls <= 0 {
		file.MaxToolCalls = maxWorkloadToolCalls
	}
	if file.MaxToolCalls > maxWorkloadToolCalls {
		return workloadFile{}, fmt.Errorf("max_tool_calls %d exceeds the hard cap of %d",
			file.MaxToolCalls, maxWorkloadToolCalls)
	}
	seen := map[string]bool{}
	for i := range file.Tasks {
		task := &file.Tasks[i]
		if task.ID == "" || task.Prompt == "" {
			return workloadFile{}, fmt.Errorf("task %q: id and prompt are required", task.ID)
		}
		if seen[task.ID] {
			return workloadFile{}, fmt.Errorf("duplicate task id %q", task.ID)
		}
		seen[task.ID] = true
		for _, category := range task.FailureCategories {
			if !failureCategories[category] {
				return workloadFile{}, fmt.Errorf("task %q: unknown failure category %q", task.ID, category)
			}
		}
		expectation, ok := expectations[task.ID]
		if !ok {
			return workloadFile{}, fmt.Errorf(
				"task %q has no generator-computed expectation (fixtures/v4/generator/expected.go)", task.ID)
		}
		task.answerContains = expectation.Contains
		task.expectedRows = expectation.Result.Rows
	}
	return file, nil
}

func runWorkload(ctx context.Context) error {
	client, err := newModelClient()
	if err != nil {
		return err
	}
	cfg := workload.DefaultConfig()
	tasksPath := envOr("EVAL_TASKS", filepath.Join("fixtures", "v4", "tasks", "tasks.yaml"))
	file, err := loadWorkloadTasks(tasksPath, workload.Expected(cfg))
	if err != nil {
		return fmt.Errorf("tasks: %w", err)
	}

	dsn := os.Getenv("EVAL_DSN")
	if dsn == "" {
		var cleanup func()
		dsn, cleanup, err = startWorkloadDatabase(ctx, cfg)
		if err != nil {
			return fmt.Errorf("database: %w", err)
		}
		defer cleanup()
	}

	configPath := envOr("EVAL_CONFIG", filepath.Join("fixtures", "v4", "profiles", "default.yaml"))
	session, closeSession, err := startServer(ctx, dsn, configPath)
	if err != nil {
		return fmt.Errorf("server: %w", err)
	}
	defer closeSession()

	report, err := runWorkloadTasks(ctx, client, session, file)
	if err != nil {
		return err
	}
	return printReport(report)
}

// startWorkloadDatabase seeds the v4 fixture from the deterministic
// generator (same source as the checked-in SQL and the expected results).
func startWorkloadDatabase(ctx context.Context, cfg workload.Config) (string, func(), error) {
	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("evalworkload"),
		postgres.WithUsername("evalworkload"),
		postgres.WithPassword("evalworkload"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = container.Terminate(context.Background()) }
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	provider, err := pgprov.New(dsn)
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	defer func() { _ = provider.Close() }()
	statements := workload.Generate(cfg).Statements(workload.DialectPostgres)
	// ANALYZE keeps the cost gate's row estimates honest on small tables.
	statements = append(statements, "ANALYZE")
	for _, stmt := range statements {
		if _, err := provider.ExecContext(ctx, stmt); err != nil {
			cleanup()
			return "", func() {}, fmt.Errorf("seed %q: %w", firstLine(stmt), err)
		}
	}
	return dsn, cleanup, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// runWorkloadTasks mirrors the regression pool with workload grading.
func runWorkloadTasks(
	ctx context.Context,
	client *modelClient,
	session *mcp.ClientSession,
	file workloadFile,
) (evalReport, error) {
	tools, err := chatToolsFromSession(ctx, session)
	if err != nil {
		return evalReport{}, fmt.Errorf("list tools: %w", err)
	}
	budget := &tokenBudget{limit: int64(envInt("EVAL_MAX_TOKENS", workloadTokenLimit))}
	report := evalReport{
		Track:          "workload",
		TaskSetVersion: file.Version,
		Model:          client.model,
		BaseURL:        client.baseURL,
		StartedAt:      time.Now().UTC(),
		TokenLimit:     budget.limit,
	}
	workers := envInt("EVAL_PARALLEL", 6)
	sem := make(chan struct{}, workers)
	results := make([]taskResult, len(file.Tasks))
	var wg sync.WaitGroup
	for i, task := range file.Tasks {
		wg.Add(1)
		go func(i int, task workloadTask) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			fmt.Fprintf(os.Stderr, "running task %s...\n", task.ID)
			tr, err := runConversation(ctx, client, session, tools, task.Prompt, file.MaxToolCalls, budget)
			results[i] = gradeWorkload(task, tr)
			if err != nil {
				results[i].Passed = false
				results[i].Error = err.Error()
				results[i].Failures = append(results[i].Failures, "conversation error")
			}
			fmt.Fprintf(os.Stderr, "task %s: passed=%v\n", task.ID, results[i].Passed)
		}(i, task)
	}
	wg.Wait()
	report.Tasks = results
	report.Aggregate = aggregateWorkload(results)
	report.TokensExceeded = budget.exhausted()
	return report, nil
}

// gradeWorkload applies both grading channels to one task transcript.
func gradeWorkload(task workloadTask, tr transcript) taskResult {
	calls := tr.toolSteps()
	result := taskResult{
		ID: task.ID, Category: task.Module, Prompt: task.Prompt,
		Module: task.Module, Capabilities: task.Capabilities, SemanticTraps: task.SemanticTraps,
		ToolCalls: len(calls), Transcript: tr.Steps, FinalAnswer: tr.FinalAnswer,
		PromptTokens: tr.Prompt, CompletionToken: tr.Completion,
	}
	for _, call := range calls {
		if call.Denied {
			result.DeniedCalls++
		}
		if call.Tool == discoveryTool && !call.Denied {
			result.DiscoveryCalls++
		}
	}
	gradeExpectTool(taskSpec{ID: task.ID, ExpectTool: task.ExpectTool}, calls, &result)

	// Mechanical channel: every generator-computed key value must appear in
	// the answer; pure numbers match on digit boundaries so small counts
	// cannot pass by accident ("6" does not match "16").
	answer := normalizeAnswer(tr.FinalAnswer)
	for _, want := range task.answerContains {
		if !answerHasValue(answer, want) {
			result.Failures = append(result.Failures, fmt.Sprintf("answer missing %q", want))
		}
	}

	// Evidence channel: how much of the expected result surfaced in tool
	// results (regardless of the final answer wording).
	result.EvidenceRowsTotal = len(task.expectedRows)
	result.EvidenceRowsFound = evidenceRowsFound(task.expectedRows, calls)

	result.Passed = len(result.Failures) == 0
	if !result.Passed {
		result.Attribution, result.AttributionConfidence = attributeFailure(task, result, calls)
	}
	return result
}

var numberValue = regexp.MustCompile(`\A-?[0-9]+(\.[0-9]+)?\z`)

// answerHasValue matches one expected value against the normalized answer.
// Numeric values require digit boundaries; other values match as
// case-insensitive substrings (same semantics as the regression track).
func answerHasValue(normalizedAnswer, want string) bool {
	want = normalizeAnswer(want)
	if !numberValue.MatchString(want) {
		return containsFold(normalizedAnswer, want)
	}
	for from := 0; ; {
		i := strings.Index(normalizedAnswer[from:], want)
		if i < 0 {
			return false
		}
		i += from
		if numericBoundaries(normalizedAnswer, i, i+len(want)) {
			return true
		}
		from = i + 1
	}
}

// numericBoundaries reports whether answer[start:end] is a standalone
// number: not preceded by a digit or decimal point, and not continued by
// more digits ("6" must not match inside "16" or "6.5", but a sentence
// period after "4589." is fine).
func numericBoundaries(answer string, start, end int) bool {
	if start > 0 {
		prev := answer[start-1]
		if prev >= '0' && prev <= '9' || prev == '.' {
			return false
		}
	}
	if end < len(answer) {
		next := answer[end]
		if next >= '0' && next <= '9' {
			return false
		}
		if next == '.' && end+1 < len(answer) {
			after := answer[end+1]
			if after >= '0' && after <= '9' {
				return false
			}
		}
	}
	return true
}

// evidenceRowsFound counts expected rows whose every cell value appears in
// some successful tool result. It is a coverage signal for manual review,
// not a pass/fail check: correct aggregation by the model can legitimately
// synthesize values that never appear verbatim in tool output.
func evidenceRowsFound(rows [][]string, calls []interactionStep) int {
	var payloads []string
	for _, call := range calls {
		if !call.Denied {
			payloads = append(payloads, normalizeAnswer(string(call.Result)))
		}
	}
	found := 0
	for _, row := range rows {
		for _, payload := range payloads {
			all := true
			for _, cell := range row {
				if !answerHasValue(payload, cell) {
					all = false
					break
				}
			}
			if all {
				found++
				break
			}
		}
	}
	return found
}

// attributeFailure assigns a failed task to the roadmap's failure outlets.
// Structural failures attribute mechanically; wrong-answer failures fall
// back to the task's declared candidate categories and are flagged for
// manual review (the v3 convention: review refines attribution, never the
// pass/fail number).
func attributeFailure(task workloadTask, result taskResult, calls []interactionStep) ([]string, string) {
	succeeded := 0
	invalidArgs := 0
	governanceDenied := 0
	for _, call := range calls {
		if !call.Denied {
			if call.Tool != discoveryTool {
				succeeded++
			}
			continue
		}
		switch call.DenialCode {
		case "COST_EXCEEDED", "BUDGET_EXCEEDED", "UNAUTHORIZED", "FORBIDDEN":
			governanceDenied++
		default:
			invalidArgs++
		}
	}
	switch {
	case len(calls) == 0 || succeeded == 0 && invalidArgs == 0 && governanceDenied == 0:
		return []string{"agent_discovery"}, "mechanical"
	case governanceDenied > 0 && succeeded == 0:
		return []string{"governance_policy"}, "mechanical"
	case invalidArgs > 0 && succeeded == 0:
		return []string{"argument_construction"}, "mechanical"
	case result.ToolCalls >= maxWorkloadToolCalls && result.FinalAnswer == "":
		// Call budget exhausted without an answer: the query likely was not
		// expressible in bounded governed calls.
		return []string{"ir_expressibility"}, "mechanical"
	case len(task.FailureCategories) > 0:
		return task.FailureCategories, "manual_review"
	default:
		return []string{"agent_discovery"}, "manual_review"
	}
}

// aggregateWorkload reuses the regression aggregate; the workload track has
// no violation tasks (governance scenarios stay on the frozen track).
func aggregateWorkload(results []taskResult) reportAggregate {
	specs := make([]taskSpec, len(results))
	return aggregate(results, specs)
}
