package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type diagnosticTaskResult struct {
	taskResult
}

type diagnosticAggregate struct {
	reportAggregate
	BehaviorAccuracy       float64 `json:"behaviorAccuracy"`
	OracleAttributionRate  float64 `json:"oracleAttributionRate"`
	UnattributedFailures   int     `json:"unattributedFailures"`
	ProductFixableFailures int     `json:"productFixableFailures"`
	ModelOnlyFailures      int     `json:"modelOnlyFailures"`
	ClarifyTasks           int     `json:"clarifyTasks"`
	DenyTasks              int     `json:"denyTasks"`
	GovernanceTasks        int     `json:"governanceTasks"`
}

type diagnosticReport struct {
	Track          string                 `json:"track"`
	TaskSetVersion int                    `json:"taskSetVersion"`
	Model          string                 `json:"model"`
	BaseURL        string                 `json:"baseUrl"`
	StartedAt      time.Time              `json:"startedAt"`
	TokenLimit     int64                  `json:"tokenLimit"`
	TokensExceeded bool                   `json:"tokenBudgetExhausted"`
	Aggregate      diagnosticAggregate    `json:"aggregate"`
	Tasks          []diagnosticTaskResult `json:"tasks"`
}

func runDiagnosticTasksLoop(
	ctx context.Context,
	client *modelClient,
	session *mcp.ClientSession,
	file diagnosticFile,
) (diagnosticReport, error) {
	tools, err := chatToolsFromSession(ctx, session)
	if err != nil {
		return diagnosticReport{}, fmt.Errorf("list tools: %w", err)
	}
	budget := &tokenBudget{limit: int64(envInt("EVAL_MAX_TOKENS", workloadTokenLimit))}
	report := diagnosticReport{
		Track: "diagnostic", TaskSetVersion: file.Version,
		Model: client.model, BaseURL: client.baseURL,
		StartedAt: time.Now().UTC(), TokenLimit: budget.limit,
	}
	results := runDiagnosticPool(ctx, client, session, tools, file, budget)
	report.Tasks = results
	report.Aggregate = aggregateDiagnostic(results, file.Tasks)
	report.TokensExceeded = budget.exhausted()
	return report, nil
}

func runDiagnosticPool(
	ctx context.Context,
	client *modelClient,
	session *mcp.ClientSession,
	tools []chatTool,
	file diagnosticFile,
	budget *tokenBudget,
) []diagnosticTaskResult {
	workers := envInt("EVAL_PARALLEL", 6)
	sem := make(chan struct{}, workers)
	results := make([]diagnosticTaskResult, len(file.Tasks))
	var wg sync.WaitGroup
	for i, task := range file.Tasks {
		wg.Add(1)
		go func(i int, task diagnosticTask) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = runOneDiagnosticTask(ctx, client, session, tools, task, file.MaxToolCalls, budget)
		}(i, task)
	}
	wg.Wait()
	return results
}

func runOneDiagnosticTask(
	ctx context.Context,
	client *modelClient,
	session *mcp.ClientSession,
	tools []chatTool,
	task diagnosticTask,
	maxCalls int,
	budget *tokenBudget,
) diagnosticTaskResult {
	fmt.Fprintf(os.Stderr, "running task %s...\n", task.ID)
	tr, err := runConversation(ctx, client, session, tools, task.Prompt, maxCalls, budget)
	base := gradeDiagnosticTask(task, tr)
	if err != nil {
		base.Passed = false
		base.Error = err.Error()
		base.Failures = append(base.Failures, "conversation error")
	}
	out := diagnosticTaskResult{taskResult: base}
	fmt.Fprintf(os.Stderr, "task %s: passed=%v\n", task.ID, out.Passed)
	return out
}

func gradeDiagnosticTask(task diagnosticTask, tr transcript) taskResult {
	switch task.ExpectedBehavior {
	case "clarify", "deny", "qualify", "unsupported":
		return gradeDiagnosticBehavior(task, tr)
	default:
		base := gradeWorkload(task.workloadTask, tr)
		return gradeAnswerBehavior(task, base)
	}
}

func aggregateDiagnostic(results []diagnosticTaskResult, tasks []diagnosticTask) diagnosticAggregate {
	specs := make([]taskSpec, len(tasks))
	for i, t := range tasks {
		specs[i] = taskSpec{Violation: t.Violation, AnswerForbids: t.AnswerForbids}
	}
	baseResults := make([]taskResult, len(results))
	for i, r := range results {
		baseResults[i] = r.taskResult
	}
	agg := diagnosticAggregate{reportAggregate: aggregate(baseResults, specs)}
	var stats diagnosticFailureStats
	for i, result := range results {
		accumulateDiagnosticTask(&agg, &stats, result, tasks[i])
	}
	finalizeDiagnosticAggregate(&agg, stats)
	return agg
}

type diagnosticFailureStats struct {
	behaviorOK, behaviorTotal               int
	oracleHits, failedTotal                 int
	unattributed, productFixable, modelOnly int
}

func accumulateDiagnosticTask(
	agg *diagnosticAggregate, stats *diagnosticFailureStats,
	result diagnosticTaskResult, t diagnosticTask,
) {
	if t.ExpectedBehavior != "answer" {
		stats.behaviorTotal++
		if result.Passed {
			stats.behaviorOK++
		}
	}
	if t.ExpectedBehavior == "clarify" {
		agg.ClarifyTasks++
	}
	if t.ExpectedBehavior == "deny" || t.Violation {
		agg.DenyTasks++
	}
	if len(t.Dimensions.GovernanceChallenges) > 0 || t.Violation {
		agg.GovernanceTasks++
	}
	if result.Passed {
		return
	}
	stats.failedTotal++
	if result.OracleMatched != "" {
		stats.oracleHits++
		stats.productFixable++
		return
	}
	if len(result.Attribution) > 0 && result.AttributionConfidence == "mechanical" {
		stats.productFixable++
		return
	}
	if len(result.Attribution) == 0 {
		stats.unattributed++
	}
	stats.modelOnly++
}

func finalizeDiagnosticAggregate(agg *diagnosticAggregate, stats diagnosticFailureStats) {
	if stats.behaviorTotal > 0 {
		agg.BehaviorAccuracy = float64(stats.behaviorOK) / float64(stats.behaviorTotal)
	}
	if stats.failedTotal > 0 {
		agg.OracleAttributionRate = float64(stats.oracleHits) / float64(stats.failedTotal)
	}
	agg.UnattributedFailures = stats.unattributed
	agg.ProductFixableFailures = stats.productFixable
	agg.ModelOnlyFailures = stats.modelOnly
}

func printDiagnosticReport(report diagnosticReport) error {
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	fmt.Fprintf(os.Stderr,
		"\npassed %d/%d; behavior accuracy %.0f%%; oracle attribution %.0f%%; "+
			"unattributed %d; product-fixable %d\n",
		report.Aggregate.TasksPassed, report.Aggregate.TasksTotal,
		report.Aggregate.BehaviorAccuracy*100,
		report.Aggregate.OracleAttributionRate*100,
		report.Aggregate.UnattributedFailures,
		report.Aggregate.ProductFixableFailures,
	)
	return nil
}

func gradeDiagnosticBehavior(task diagnosticTask, tr transcript) taskResult {
	calls := tr.toolSteps()
	result := taskResult{
		ID: task.ID, Category: task.Module, Prompt: task.Prompt,
		Module: task.Module, Capabilities: task.Capabilities,
		SemanticTraps: task.SemanticTraps,
		ToolCalls:     len(calls), Transcript: tr.Steps, FinalAnswer: tr.FinalAnswer,
		PromptTokens: tr.Prompt, CompletionToken: tr.Completion,
		PromptLevel: task.PromptLevel, ExpectedBehavior: task.ExpectedBehavior,
		Dimensions: task.Dimensions, Role: task.Role,
	}
	for _, call := range calls {
		if call.Denied {
			result.DeniedCalls++
		}
		if call.Tool == discoveryTool && !call.Denied {
			result.DiscoveryCalls++
		}
	}
	switch task.ExpectedBehavior {
	case "clarify":
		result = gradeClarifyBehavior(task, result)
	case "deny":
		result = gradeDenyBehavior(task, result)
	case "qualify":
		result = gradeQualifyBehavior(task, result)
	case "unsupported":
		result = gradeUnsupportedBehavior(task, result)
	}
	if !result.Passed {
		result.Attribution, result.AttributionConfidence = attributeFailure(
			task.workloadTask, result, calls)
	}
	return result
}
