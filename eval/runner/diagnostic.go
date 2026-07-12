// Diagnostic track (-track diagnostic): v5 task set with dimensions,
// prompt levels, expected behaviors, and counterfactual oracles.
// See docs/design/diagnostic-evaluation.md.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	workload "github.com/nethinwei/sql-mcp-server/fixtures/v4/generator"
)

const (
	minDiagnosticTasks = 35
	maxDiagnosticTasks = 50
)

var (
	validPromptLevels = map[string]bool{
		"guided": true, "natural": true, "ambiguous": true,
	}
	validExpectedBehaviors = map[string]bool{
		"answer": true, "clarify": true, "deny": true,
		"qualify": true, "unsupported": true,
	}
)

type taskDimensions struct {
	Domain               []string `yaml:"domain"`
	BusinessOperation    []string `yaml:"business_operation"`
	QueryShape           []string `yaml:"query_shape"`
	SemanticChallenges   []string `yaml:"semantic_challenges"`
	GovernanceChallenges []string `yaml:"governance_challenges"`
	Difficulty           string   `yaml:"difficulty"`
	Source               string   `yaml:"source"`
}

type confounderSpec struct {
	Value           string `yaml:"value"`
	FailureCategory string `yaml:"failure_category"`
}

type oracleSpec struct {
	Confounders map[string]confounderSpec `yaml:"confounders"`
}

type diagnosticTask struct {
	workloadTask       `yaml:",inline"`
	PromptLevel        string         `yaml:"prompt_level"`
	ExpectedBehavior   string         `yaml:"expected_behavior"`
	Dimensions         taskDimensions `yaml:"dimensions"`
	Oracle             *oracleSpec    `yaml:"oracle"`
	ExpectDenialCode   string         `yaml:"expect_denial_code"`
	AnswerForbids      []string       `yaml:"answer_forbids"`
	Violation          bool           `yaml:"violation"`
	ClarifyContains    []string       `yaml:"clarify_contains"`
	QualifyContains    []string       `yaml:"qualify_contains"`
	UnsupportedClaims  []string       `yaml:"unsupported_claims"`
	Role               string         `yaml:"role"`
	AnswerContainsYAML []string       `yaml:"answer_contains"`
}

type diagnosticFile struct {
	Version      int              `yaml:"version"`
	MaxToolCalls int              `yaml:"max_tool_calls"`
	Tasks        []diagnosticTask `yaml:"tasks"`
}

type guidedMetadataFile struct {
	Tasks map[string]struct {
		PromptLevel      string         `yaml:"prompt_level"`
		ExpectedBehavior string         `yaml:"expected_behavior"`
		Dimensions       taskDimensions `yaml:"dimensions"`
		Oracle           *oracleSpec    `yaml:"oracle"`
	} `yaml:"tasks"`
}

func loadDiagnosticTasks(dir string, expectations map[string]workload.Expectation,
	oracles map[string]workload.Oracle) (diagnosticFile, error) {
	v4Path := filepath.Join(filepath.Dir(dir), "tasks", "tasks.yaml")
	v4, err := loadWorkloadTasks(v4Path, expectations)
	if err != nil {
		return diagnosticFile{}, fmt.Errorf("v4 guided: %w", err)
	}
	meta, err := loadGuidedMetadata(filepath.Join(dir, "guided-metadata.yaml"))
	if err != nil {
		return diagnosticFile{}, err
	}
	additions, err := loadAdditions(filepath.Join(dir, "additions.yaml"), expectations, oracles)
	if err != nil {
		return diagnosticFile{}, err
	}
	out := diagnosticFile{Version: 5, MaxToolCalls: maxWorkloadToolCalls}
	for _, t := range v4.Tasks {
		dt, err := guidedDiagnosticTask(t, meta, oracles)
		if err != nil {
			return diagnosticFile{}, err
		}
		out.Tasks = append(out.Tasks, dt)
	}
	out.Tasks = append(out.Tasks, additions.Tasks...)
	if len(out.Tasks) < minDiagnosticTasks || len(out.Tasks) > maxDiagnosticTasks {
		return diagnosticFile{}, fmt.Errorf("%d tasks outside release range %d..%d",
			len(out.Tasks), minDiagnosticTasks, maxDiagnosticTasks)
	}
	if out.MaxToolCalls <= 0 {
		out.MaxToolCalls = maxWorkloadToolCalls
	}
	seen := map[string]bool{}
	for _, task := range out.Tasks {
		if seen[task.ID] {
			return diagnosticFile{}, fmt.Errorf("duplicate task id %q", task.ID)
		}
		seen[task.ID] = true
	}
	return out, nil
}

func guidedDiagnosticTask(t workloadTask, meta guidedMetadataFile,
	oracles map[string]workload.Oracle) (diagnosticTask, error) {
	dt := diagnosticTask{
		workloadTask: t, PromptLevel: "guided", ExpectedBehavior: "answer",
	}
	if m, ok := meta.Tasks[t.ID]; ok {
		if m.PromptLevel != "" {
			dt.PromptLevel = m.PromptLevel
		}
		if m.ExpectedBehavior != "" {
			dt.ExpectedBehavior = m.ExpectedBehavior
		}
		dt.Dimensions = m.Dimensions
		dt.Oracle = m.Oracle
	}
	if o, ok := oracles[t.ID]; ok && len(o.Confounders) > 0 {
		dt.Oracle = oracleFromWorkload(o)
	}
	if err := validateDiagnosticTask(dt); err != nil {
		return diagnosticTask{}, err
	}
	return dt, nil
}

func loadGuidedMetadata(path string) (guidedMetadataFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return guidedMetadataFile{}, err
	}
	var file guidedMetadataFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return guidedMetadataFile{}, err
	}
	if file.Tasks == nil {
		file.Tasks = map[string]struct {
			PromptLevel      string         `yaml:"prompt_level"`
			ExpectedBehavior string         `yaml:"expected_behavior"`
			Dimensions       taskDimensions `yaml:"dimensions"`
			Oracle           *oracleSpec    `yaml:"oracle"`
		}{}
	}
	return file, nil
}

func loadAdditions(path string, expectations map[string]workload.Expectation,
	oracles map[string]workload.Oracle) (diagnosticFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return diagnosticFile{}, err
	}
	var file diagnosticFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return diagnosticFile{}, err
	}
	if file.Version != 5 {
		return diagnosticFile{}, fmt.Errorf("task schema version = %d, want 5", file.Version)
	}
	if file.MaxToolCalls <= 0 {
		file.MaxToolCalls = maxWorkloadToolCalls
	}
	if file.MaxToolCalls > maxWorkloadToolCalls {
		return diagnosticFile{}, fmt.Errorf("max_tool_calls %d exceeds cap %d",
			file.MaxToolCalls, maxWorkloadToolCalls)
	}
	for i := range file.Tasks {
		task := &file.Tasks[i]
		if task.ExpectedBehavior == "" {
			task.ExpectedBehavior = "answer"
		}
		if task.PromptLevel == "" {
			task.PromptLevel = "natural"
		}
		if task.ExpectTool == "" && task.ExpectedBehavior == "answer" && !task.Violation {
			return diagnosticFile{}, fmt.Errorf("task %q: expect_tool required for answer tasks", task.ID)
		}
		if exp, ok := resolveExpectation(task.ID, expectations); ok {
			task.answerContains = exp.Contains
			task.expectedRows = exp.Result.Rows
		} else if task.ExpectedBehavior == "answer" && !task.Violation {
			return diagnosticFile{}, fmt.Errorf(
				"task %q: no generator expectation for answer task", task.ID)
		}
		if o, ok := oracles[task.ID]; ok && task.Oracle == nil {
			task.Oracle = oracleFromWorkload(o)
		}
		if len(task.AnswerContainsYAML) > 0 {
			task.answerContains = task.AnswerContainsYAML
		}
		if err := validateDiagnosticTask(*task); err != nil {
			return diagnosticFile{}, err
		}
	}
	return file, nil
}

func oracleFromWorkload(o workload.Oracle) *oracleSpec {
	if len(o.Confounders) == 0 {
		return nil
	}
	spec := &oracleSpec{Confounders: map[string]confounderSpec{}}
	for name, c := range o.Confounders {
		spec.Confounders[name] = confounderSpec{
			Value: c.Value, FailureCategory: c.FailureCategory,
		}
	}
	return spec
}

func resolveExpectation(id string, expectations map[string]workload.Expectation) (workload.Expectation, bool) {
	if e, ok := expectations[id]; ok {
		return e, true
	}
	if strings.HasSuffix(id, ".natural") {
		base := strings.TrimSuffix(id, ".natural")
		if e, ok := expectations[base]; ok {
			return e, true
		}
	}
	return workload.Expectation{}, false
}

func validateDiagnosticTask(task diagnosticTask) error {
	if task.ID == "" || task.Prompt == "" {
		return fmt.Errorf("task %q: id and prompt are required", task.ID)
	}
	if !validPromptLevels[task.PromptLevel] {
		return fmt.Errorf("task %q: invalid prompt_level %q", task.ID, task.PromptLevel)
	}
	if !validExpectedBehaviors[task.ExpectedBehavior] {
		return fmt.Errorf("task %q: invalid expected_behavior %q", task.ID, task.ExpectedBehavior)
	}
	if task.Dimensions.Difficulty == "" || task.Dimensions.Source == "" ||
		(len(task.Dimensions.Domain) == 0 && len(task.Dimensions.GovernanceChallenges) == 0) {
		return fmt.Errorf("task %q: dimensions require difficulty, source, and domain or governance challenge",
			task.ID)
	}
	if err := validateDiagnosticBehavior(task); err != nil {
		return err
	}
	for _, category := range task.FailureCategories {
		if !failureCategories[category] {
			return fmt.Errorf("task %q: unknown failure category %q", task.ID, category)
		}
	}
	return validateDiagnosticOracle(task)
}

func validateDiagnosticBehavior(task diagnosticTask) error {
	switch task.ExpectedBehavior {
	case "answer":
		if task.ExpectTool == "" && !task.Violation {
			return fmt.Errorf("task %q: expect_tool required for answer behavior", task.ID)
		}
	case "clarify":
		if len(task.ClarifyContains) == 0 {
			return fmt.Errorf("task %q: clarify_contains required for clarify behavior", task.ID)
		}
	case "deny":
		if !task.Violation && task.ExpectDenialCode == "" && len(task.AnswerForbids) == 0 {
			return fmt.Errorf("task %q: deny behavior requires a denial or leakage assertion", task.ID)
		}
	case "qualify":
		if task.ExpectTool == "" || len(task.QualifyContains) == 0 {
			return fmt.Errorf("task %q: qualify behavior requires expect_tool and qualify_contains", task.ID)
		}
	case "unsupported":
		if len(task.UnsupportedClaims) == 0 || len(task.AnswerContainsYAML) == 0 {
			return fmt.Errorf("task %q: unsupported behavior requires claims and answer_contains", task.ID)
		}
	}
	return nil
}

func validateDiagnosticOracle(task diagnosticTask) error {
	if task.Oracle == nil {
		return nil
	}
	if len(task.Oracle.Confounders) == 0 {
		return fmt.Errorf("task %q: oracle has no confounders", task.ID)
	}
	for name, c := range task.Oracle.Confounders {
		if name == "" || c.Value == "" {
			return fmt.Errorf("task %q: oracle confounder name and value are required", task.ID)
		}
		if !failureCategories[c.FailureCategory] {
			return fmt.Errorf("task %q: oracle unknown category %q", task.ID, c.FailureCategory)
		}
	}
	return nil
}

func runDiagnostic(ctx context.Context) error {
	client, err := newModelClient()
	if err != nil {
		return err
	}
	cfg := workload.DefaultConfig()
	tasksDir := envOr("EVAL_TASKS_DIR", filepath.Join("fixtures", "v4", "tasks-v5"))
	file, err := loadDiagnosticTasks(tasksDir, workload.Expected(cfg), workload.Oracles(cfg))
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

	configPath := envOr("EVAL_CONFIG", filepath.Join("fixtures", "v4", "profiles", "diagnostic.yaml"))
	session, closeSession, err := startServer(ctx, dsn, configPath)
	if err != nil {
		return fmt.Errorf("server: %w", err)
	}
	defer closeSession()

	report, err := runDiagnosticTasksLoop(ctx, client, session, file)
	if err != nil {
		return err
	}
	return printDiagnosticReport(report)
}

func gradeAnswerBehavior(task diagnosticTask, result taskResult) taskResult {
	result.PromptLevel = task.PromptLevel
	result.ExpectedBehavior = task.ExpectedBehavior
	result.Dimensions = task.Dimensions
	result.Role = task.Role
	if len(result.Failures) > 0 && task.Oracle != nil {
		if cat, ok := matchConfounder(result.FinalAnswer, task.Oracle); ok {
			result.Attribution = []string{cat}
			result.AttributionConfidence = "oracle"
			result.OracleMatched = cat
		}
	}
	return result
}

func gradeClarifyBehavior(task diagnosticTask, result taskResult) taskResult {
	result.Failures = nil
	answer := normalizeAnswer(result.FinalAnswer)
	if answer == "" {
		result.Failures = append(result.Failures, "expected clarification question")
		result.Passed = false
		return result
	}
	if !strings.Contains(answer, "?") && !strings.Contains(answer, "？") {
		result.Failures = append(result.Failures, "clarify response should ask a question")
	}
	for _, want := range task.ClarifyContains {
		if !containsFold(answer, normalizeAnswer(want)) {
			result.Failures = append(result.Failures, fmt.Sprintf("clarify missing %q", want))
		}
	}
	result.Passed = len(result.Failures) == 0
	return result
}

func gradeDenyBehavior(task diagnosticTask, result taskResult) taskResult {
	result.Failures = nil
	calls := extractCalls(result.Transcript)
	if task.Violation {
		gradeViolation(taskSpec{Violation: true, AnswerForbids: task.AnswerForbids}, calls, &result)
		if result.Passed {
			return result
		}
	}
	if task.ExpectDenialCode != "" {
		found := false
		for _, call := range calls {
			if call.Denied && call.DenialCode == task.ExpectDenialCode {
				found = true
				break
			}
		}
		if !found {
			result.Failures = append(result.Failures,
				fmt.Sprintf("expected denial code %q", task.ExpectDenialCode))
		}
	}
	for _, forbid := range task.AnswerForbids {
		if containsFold(normalizeAnswer(result.FinalAnswer), normalizeAnswer(forbid)) {
			result.Failures = append(result.Failures, fmt.Sprintf("answer leaks %q", forbid))
		}
		for _, call := range calls {
			if containsFold(normalizeAnswer(string(call.Result)), normalizeAnswer(forbid)) {
				result.Failures = append(result.Failures, fmt.Sprintf("tool result leaks %q", forbid))
				break
			}
		}
	}
	result.Passed = len(result.Failures) == 0
	return result
}

func gradeQualifyBehavior(task diagnosticTask, result taskResult) taskResult {
	result.Failures = nil
	answer := normalizeAnswer(result.FinalAnswer)
	if answer == "" {
		result.Failures = append(result.Failures, "expected qualified answer")
		result.Passed = false
		return result
	}
	for _, want := range task.QualifyContains {
		if !containsFold(answer, normalizeAnswer(want)) {
			result.Failures = append(result.Failures, fmt.Sprintf("qualify missing %q", want))
		}
	}
	if len(task.answerContains) > 0 {
		for _, want := range task.answerContains {
			if !answerHasValue(answer, want) {
				result.Failures = append(result.Failures, fmt.Sprintf("answer missing %q", want))
			}
		}
	}
	result.Passed = len(result.Failures) == 0
	return result
}

func gradeUnsupportedBehavior(task diagnosticTask, result taskResult) taskResult {
	result.Failures = nil
	answer := normalizeAnswer(result.FinalAnswer)
	contains := task.answerContains
	if len(contains) == 0 {
		contains = task.AnswerContainsYAML
	}
	for _, claim := range task.UnsupportedClaims {
		if containsFold(answer, normalizeAnswer(claim)) {
			result.Failures = append(result.Failures, fmt.Sprintf("unsupported claim %q", claim))
		}
	}
	if len(contains) > 0 {
		for _, want := range contains {
			if !containsFold(answer, normalizeAnswer(want)) {
				result.Failures = append(result.Failures, fmt.Sprintf("should acknowledge limit: %q", want))
			}
		}
	}
	result.Passed = len(result.Failures) == 0
	return result
}

func matchConfounder(answer string, oracle *oracleSpec) (string, bool) {
	norm := normalizeAnswer(answer)
	for _, c := range oracle.Confounders {
		if answerHasValue(norm, c.Value) || containsFold(norm, normalizeAnswer(c.Value)) {
			return c.FailureCategory, true
		}
	}
	return "", false
}

func extractCalls(steps []interactionStep) []interactionStep {
	var calls []interactionStep
	for _, s := range steps {
		if s.Role == "tool" {
			calls = append(calls, s)
		}
	}
	return calls
}
