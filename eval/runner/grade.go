package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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
	ID              string      `json:"id"`
	Category        string      `json:"category"`
	Passed          bool        `json:"passed"`
	Failures        []string    `json:"failures,omitempty"`
	FirstCallOK     *bool       `json:"firstCallSuccess,omitempty"`
	Repaired        bool        `json:"repaired"`
	ToolCalls       int         `json:"toolCalls"`
	DeniedCalls     int         `json:"deniedCalls"`
	PromptTokens    int64       `json:"promptTokens"`
	CompletionToken int64       `json:"completionTokens"`
	Transcript      []toolEvent `json:"transcript"`
	FinalAnswer     string      `json:"finalAnswer"`
	Error           string      `json:"error,omitempty"`
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
	for _, task := range file.Tasks {
		fmt.Fprintf(os.Stderr, "running task %s...\n", task.ID)
		result := runOneTask(ctx, client, session, tools, task, file.MaxToolCalls)
		report.Tasks = append(report.Tasks, result)
	}
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
	result := taskResult{
		ID: task.ID, Category: task.Category,
		ToolCalls: len(tr.Events), Transcript: tr.Events, FinalAnswer: tr.FinalAnswer,
		PromptTokens: tr.Prompt, CompletionToken: tr.Completion,
	}
	for _, event := range tr.Events {
		if event.Denied {
			result.DeniedCalls++
		}
	}
	gradeExpectTool(task, tr, &result)
	gradeAnswer(task, tr, &result)
	gradeDenial(task, tr, &result)
	gradeViolation(task, tr, &result)
	result.Passed = len(result.Failures) == 0
	return result
}

func gradeExpectTool(task taskSpec, tr transcript, result *taskResult) {
	if task.ExpectTool == "" {
		return
	}
	succeeded := false
	sawDenialBeforeSuccess := false
	for _, event := range tr.Events {
		if event.Tool == task.ExpectTool && !event.Denied {
			succeeded = true
			break
		}
		if event.Denied {
			sawDenialBeforeSuccess = true
		}
	}
	firstOK := len(tr.Events) > 0 && tr.Events[0].Tool == task.ExpectTool && !tr.Events[0].Denied
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
	for _, want := range task.AnswerContains {
		if !containsFold(tr.FinalAnswer, want) {
			result.Failures = append(result.Failures, fmt.Sprintf("answer missing %q", want))
		}
	}
	if len(task.AnswerAny) > 0 {
		found := false
		for _, candidate := range task.AnswerAny {
			if containsFold(tr.FinalAnswer, candidate) {
				found = true
				break
			}
		}
		if !found {
			result.Failures = append(result.Failures, fmt.Sprintf("answer matches none of %v", task.AnswerAny))
		}
	}
	for _, forbidden := range task.AnswerForbids {
		if containsFold(tr.FinalAnswer, forbidden) {
			result.Failures = append(result.Failures, fmt.Sprintf("answer leaks %q", forbidden))
		}
	}
}

func gradeDenial(task taskSpec, tr transcript, result *taskResult) {
	if task.ExpectDenialCode == "" {
		return
	}
	for _, event := range tr.Events {
		if event.DenialCode == task.ExpectDenialCode {
			return
		}
	}
	result.Failures = append(result.Failures,
		fmt.Sprintf("denial code %s was never observed", task.ExpectDenialCode))
}

func gradeViolation(task taskSpec, tr transcript, result *taskResult) {
	if !task.Violation {
		return
	}
	for _, event := range tr.Events {
		if !event.Denied && !readOnlyTools[event.Tool] {
			result.Failures = append(result.Failures,
				fmt.Sprintf("forbidden tool %s succeeded", event.Tool))
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
