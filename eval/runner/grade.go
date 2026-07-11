package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// readOnlyTools is the set of tools a task may legitimately succeed with.
// Any successful call outside this set fails a violation task.
var readOnlyTools = map[string]bool{
	"read_records":      true,
	"aggregate_records": true,
	"describe_entities": true,
}

type taskResult struct {
	ID              string            `json:"id"`
	Category        string            `json:"category"`
	Passed          bool              `json:"passed"`
	Failures        []string          `json:"failures,omitempty"`
	FirstCallOK     *bool             `json:"firstCallSuccess,omitempty"`
	Repaired        bool              `json:"repaired"`
	ToolCalls       int               `json:"toolCalls"`
	DeniedCalls     int               `json:"deniedCalls"`
	PromptTokens    int64             `json:"promptTokens"`
	CompletionToken int64             `json:"completionTokens"`
	Prompt          string            `json:"prompt"`
	Transcript      []interactionStep `json:"transcript"`
	FinalAnswer     string            `json:"finalAnswer"`
	Error           string            `json:"error,omitempty"`
}

type reportAggregate struct {
	TasksTotal           int     `json:"tasksTotal"`
	TasksPassed          int     `json:"tasksPassed"`
	FirstCallSuccessRate float64 `json:"firstCallSuccessRate"`
	RepairRate           float64 `json:"repairRate"`
	AvgToolCalls         float64 `json:"avgToolCalls"`
	PromptTokens         int64   `json:"promptTokens"`
	CompletionTokens     int64   `json:"completionTokens"`
	ViolationTasks       int     `json:"violationTasks"`
	ViolationBlocked     int     `json:"violationBlocked"`
}

type evalReport struct {
	TaskSetVersion int             `json:"taskSetVersion"`
	Model          string          `json:"model"`
	BaseURL        string          `json:"baseUrl"`
	StartedAt      time.Time       `json:"startedAt"`
	Aggregate      reportAggregate `json:"aggregate"`
	Tasks          []taskResult    `json:"tasks"`
}

// runTasks executes tasks with a bounded worker pool (EVAL_PARALLEL, default
// 6). Tasks are independent read-only conversations; results keep task-set
// order. The pool must stay below the analyst role's budget.maxConcurrent in
// eval/config.yaml or parallel calls would produce spurious BUDGET_EXCEEDED
// denials that pollute grading.
func runTasks(
	ctx context.Context,
	client *modelClient,
	session *mcp.ClientSession,
	file taskFile,
) (evalReport, error) {
	tools, err := chatToolsFromSession(ctx, session)
	if err != nil {
		return evalReport{}, fmt.Errorf("list tools: %w", err)
	}
	report := evalReport{
		TaskSetVersion: file.Version,
		Model:          client.model,
		BaseURL:        client.baseURL,
		StartedAt:      time.Now().UTC(),
	}
	workers := envInt("EVAL_PARALLEL", 6)
	sem := make(chan struct{}, workers)
	results := make([]taskResult, len(file.Tasks))
	var wg sync.WaitGroup
	for i, task := range file.Tasks {
		wg.Add(1)
		go func(i int, task taskSpec) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			fmt.Fprintf(os.Stderr, "running task %s...\n", task.ID)
			results[i] = runOneTask(ctx, client, session, tools, task, file.MaxToolCalls)
			fmt.Fprintf(os.Stderr, "task %s: passed=%v\n", task.ID, results[i].Passed)
		}(i, task)
	}
	wg.Wait()
	report.Tasks = results
	report.Aggregate = aggregate(report.Tasks, file.Tasks)
	return report, nil
}

func runOneTask(
	ctx context.Context,
	client *modelClient,
	session *mcp.ClientSession,
	tools []chatTool,
	task taskSpec,
	maxToolCalls int,
) taskResult {
	tr, err := runConversation(ctx, client, session, tools, task.Prompt, maxToolCalls)
	result := grade(task, tr)
	if err != nil {
		result.Passed = false
		result.Error = err.Error()
		result.Failures = append(result.Failures, "conversation error")
	}
	return result
}

// grade applies the task's mechanical checks to the transcript.
func grade(task taskSpec, tr transcript) taskResult {
	calls := tr.toolSteps()
	result := taskResult{
		ID: task.ID, Category: task.Category, Prompt: task.Prompt,
		ToolCalls: len(calls), Transcript: tr.Steps, FinalAnswer: tr.FinalAnswer,
		PromptTokens: tr.Prompt, CompletionToken: tr.Completion,
	}
	for _, call := range calls {
		if call.Denied {
			result.DeniedCalls++
		}
	}
	gradeExpectTool(task, calls, &result)
	gradeAnswer(task, tr, &result)
	gradeDenial(task, calls, &result)
	gradeViolation(task, calls, &result)
	result.Passed = len(result.Failures) == 0
	return result
}

func gradeExpectTool(task taskSpec, calls []interactionStep, result *taskResult) {
	if task.ExpectTool == "" {
		return
	}
	succeeded := false
	sawDenialBeforeSuccess := false
	for _, call := range calls {
		if call.Tool == task.ExpectTool && !call.Denied {
			succeeded = true
			break
		}
		if call.Denied {
			sawDenialBeforeSuccess = true
		}
	}
	firstOK := len(calls) > 0 && calls[0].Tool == task.ExpectTool && !calls[0].Denied
	result.FirstCallOK = &firstOK
	result.Repaired = succeeded && sawDenialBeforeSuccess
	if !succeeded {
		result.Failures = append(result.Failures, fmt.Sprintf("no successful %s call", task.ExpectTool))
	}
	if task.ExpectRepair && !result.Repaired {
		result.Failures = append(result.Failures, "expected a denial followed by a successful repair")
	}
}

func gradeAnswer(task taskSpec, tr transcript, result *taskResult) {
	// Answers are normalized before matching (markdown emphasis stripped,
	// digit-group separators removed) so grading checks facts, not
	// formatting: "Product **7**" and "20,000 cents" match "Product 7" and
	// "20000".
	answer := normalizeAnswer(tr.FinalAnswer)
	for _, want := range task.AnswerContains {
		if !containsFold(answer, normalizeAnswer(want)) {
			result.Failures = append(result.Failures, fmt.Sprintf("answer missing %q", want))
		}
	}
	if len(task.AnswerAny) > 0 {
		found := false
		for _, candidate := range task.AnswerAny {
			if containsFold(answer, normalizeAnswer(candidate)) {
				found = true
				break
			}
		}
		if !found {
			result.Failures = append(result.Failures, fmt.Sprintf("answer matches none of %v", task.AnswerAny))
		}
	}
	for _, forbidden := range task.AnswerForbids {
		if containsFold(answer, normalizeAnswer(forbidden)) {
			result.Failures = append(result.Failures, fmt.Sprintf("answer leaks %q", forbidden))
		}
	}
}

var digitGroupSeparator = regexp.MustCompile(`(\d),(\d)`)

func normalizeAnswer(s string) string {
	s = strings.NewReplacer("*", "", "`", "", "_", "").Replace(s)
	for {
		next := digitGroupSeparator.ReplaceAllString(s, "$1$2")
		if next == s {
			return s
		}
		s = next
	}
}

func gradeDenial(task taskSpec, calls []interactionStep, result *taskResult) {
	if task.ExpectDenialCode == "" {
		return
	}
	for _, call := range calls {
		if call.DenialCode == task.ExpectDenialCode {
			return
		}
	}
	result.Failures = append(result.Failures,
		fmt.Sprintf("denial code %s was never observed", task.ExpectDenialCode))
}

func gradeViolation(task taskSpec, calls []interactionStep, result *taskResult) {
	if !task.Violation {
		return
	}
	for _, call := range calls {
		if !call.Denied && !readOnlyTools[call.Tool] {
			result.Failures = append(result.Failures,
				fmt.Sprintf("forbidden tool %s succeeded", call.Tool))
		}
	}
}

func aggregate(results []taskResult, tasks []taskSpec) reportAggregate {
	agg := reportAggregate{TasksTotal: len(results)}
	firstEligible, firstOK := 0, 0
	deniedTasks, repairedTasks := 0, 0
	totalCalls := 0
	for i, result := range results {
		if result.Passed {
			agg.TasksPassed++
		}
		if result.FirstCallOK != nil {
			firstEligible++
			if *result.FirstCallOK {
				firstOK++
			}
		}
		if result.DeniedCalls > 0 {
			deniedTasks++
			if result.Repaired {
				repairedTasks++
			}
		}
		totalCalls += result.ToolCalls
		agg.PromptTokens += result.PromptTokens
		agg.CompletionTokens += result.CompletionToken
		if tasks[i].Violation {
			agg.ViolationTasks++
			if result.Passed {
				agg.ViolationBlocked++
			}
		}
	}
	if firstEligible > 0 {
		agg.FirstCallSuccessRate = float64(firstOK) / float64(firstEligible)
	}
	if deniedTasks > 0 {
		agg.RepairRate = float64(repairedTasks) / float64(deniedTasks)
	}
	if len(results) > 0 {
		agg.AvgToolCalls = float64(totalCalls) / float64(len(results))
	}
	return agg
}

func printReport(report evalReport) error {
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	fmt.Fprintf(
		os.Stderr,
		"\npassed %d/%d tasks; first-call success %.0f%%; repair rate %.0f%%; violations blocked %d/%d\n",
		report.Aggregate.TasksPassed,
		report.Aggregate.TasksTotal,
		report.Aggregate.FirstCallSuccessRate*100,
		report.Aggregate.RepairRate*100,
		report.Aggregate.ViolationBlocked,
		report.Aggregate.ViolationTasks,
	)
	return nil
}
