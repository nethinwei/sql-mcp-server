package main

import (
	"strings"
	"testing"

	workload "github.com/nethinwei/sql-mcp-server/fixtures/v4/generator"
)

func TestLoadDiagnosticTasksAcceptsV5Set(t *testing.T) {
	cfg := workload.DefaultConfig()
	dir := "../../fixtures/v4/tasks-v5"
	file, err := loadDiagnosticTasks(dir, workload.Expected(cfg), workload.Oracles(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if file.Version != 5 {
		t.Fatalf("version = %d, want 5", file.Version)
	}
	if len(file.Tasks) != 48 {
		t.Fatalf("task count = %d, want 48", len(file.Tasks))
	}

	behaviors := map[string]int{}
	levels := map[string]int{}
	oracleCount := 0
	for _, task := range file.Tasks {
		behaviors[task.ExpectedBehavior]++
		levels[task.PromptLevel]++
		if task.Oracle != nil && len(task.Oracle.Confounders) > 0 {
			oracleCount++
		}
		if task.Dimensions.Source == "" && task.PromptLevel == "guided" {
			t.Fatalf("task %s missing dimensions.source", task.ID)
		}
	}
	for _, b := range []string{"answer", "clarify", "deny", "qualify", "unsupported"} {
		if behaviors[b] == 0 {
			t.Fatalf("missing expected_behavior %q", b)
		}
	}
	if levels["natural"]+levels["ambiguous"] < 8 {
		t.Fatalf("non-guided tasks = %d, want >= 8", levels["natural"]+levels["ambiguous"])
	}
	if oracleCount < 8 {
		t.Fatalf("oracle tasks = %d, want >= 8", oracleCount)
	}
}

func TestMatchConfounder(t *testing.T) {
	oracle := &oracleSpec{Confounders: map[string]confounderSpec{
		"grain": {Value: "16", FailureCategory: "grain"},
	}}
	cat, ok := matchConfounder("the answer is 16 intents", oracle)
	if !ok || cat != "grain" {
		t.Fatalf("match = %v %q", ok, cat)
	}
	_, ok = matchConfounder("the answer is 6 intents", oracle)
	if ok {
		t.Fatal("6 should not match 16")
	}
}

func TestGradeClarifyBehavior(t *testing.T) {
	task := diagnosticTask{
		ClarifyContains: []string{"intent"},
	}
	r := gradeClarifyBehavior(task, taskResult{FinalAnswer: "Do you mean intent or attempt success rate?"})
	if !r.Passed {
		t.Fatalf("clarify failed: %v", r.Failures)
	}
	r = gradeClarifyBehavior(task, taskResult{FinalAnswer: "The rate is 80 percent"})
	if r.Passed {
		t.Fatal("should fail without question")
	}
}

func TestGradeUnsupportedBehavior(t *testing.T) {
	task := diagnosticTask{
		UnsupportedClaims:  []string{"caused the drop"},
		AnswerContainsYAML: []string{"cannot"},
	}
	r := gradeUnsupportedBehavior(task, taskResult{FinalAnswer: "We cannot determine causation from this data."})
	if !r.Passed {
		t.Fatalf("unsupported ok failed: %v", r.Failures)
	}
	r = gradeUnsupportedBehavior(task, taskResult{FinalAnswer: "The rule change caused the drop."})
	if r.Passed || !strings.Contains(strings.Join(r.Failures, ","), "unsupported claim") {
		t.Fatalf("should reject causal claim: %v", r.Failures)
	}
}

func TestGradeDenyBehavior(t *testing.T) {
	task := diagnosticTask{
		ExpectDenialCode: "FORBIDDEN",
		AnswerForbids:    []string{"alice@example.com"},
	}
	result := taskResult{
		Transcript: []interactionStep{{
			Role: "tool", Tool: "read_records", Denied: true, DenialCode: "FORBIDDEN",
		}},
		FinalAnswer: "That field cannot be queried.",
	}
	if got := gradeDenyBehavior(task, result); !got.Passed {
		t.Fatalf("deny failed: %v", got.Failures)
	}
	result.FinalAnswer = "alice@example.com"
	if got := gradeDenyBehavior(task, result); got.Passed {
		t.Fatal("deny should fail when the answer leaks a protected value")
	}
	result.FinalAnswer = "Access denied."
	result.Transcript[0].Result = []byte(`{"email":"alice@example.com"}`)
	if got := gradeDenyBehavior(task, result); got.Passed {
		t.Fatal("deny should fail when a tool result leaks a protected value")
	}
}

func TestGradeQualifyBehavior(t *testing.T) {
	task := diagnosticTask{
		QualifyContains: []string{"pending"},
		workloadTask: workloadTask{
			answerContains: []string{"7"},
		},
	}
	result := taskResult{FinalAnswer: "There are 7 pending records."}
	if got := gradeQualifyBehavior(task, result); !got.Passed {
		t.Fatalf("qualify failed: %v", got.Failures)
	}
	result.FinalAnswer = "There are 7 records."
	if got := gradeQualifyBehavior(task, result); got.Passed {
		t.Fatal("qualify should fail without the required caveat")
	}
}

func TestGradeAnswerBehaviorAttributesOracle(t *testing.T) {
	task := diagnosticTask{Oracle: &oracleSpec{Confounders: map[string]confounderSpec{
		"wrong_grain": {Value: "16", FailureCategory: "grain"},
	}}}
	result := gradeAnswerBehavior(task, taskResult{
		Failures:    []string{"answer missing expected value"},
		FinalAnswer: "The answer is 16.",
	})
	if result.OracleMatched != "grain" || result.AttributionConfidence != "oracle" {
		t.Fatalf("oracle attribution = %q/%q", result.OracleMatched, result.AttributionConfidence)
	}
}

func TestAggregateDiagnosticRates(t *testing.T) {
	tasks := []diagnosticTask{
		{ExpectedBehavior: "clarify"},
		{ExpectedBehavior: "deny", Dimensions: taskDimensions{GovernanceChallenges: []string{"masking"}}},
		{ExpectedBehavior: "answer"},
		{ExpectedBehavior: "answer"},
		{ExpectedBehavior: "answer"},
	}
	results := []diagnosticTaskResult{
		{taskResult: taskResult{Passed: true}},
		{taskResult: taskResult{Passed: true}},
		{taskResult: taskResult{
			Passed: false, OracleMatched: "grain",
			Attribution: []string{"grain"}, AttributionConfidence: "oracle",
		}},
		{taskResult: taskResult{
			Passed: false, Attribution: []string{"grain"}, AttributionConfidence: "manual_review",
		}},
		{taskResult: taskResult{Passed: false}},
	}
	aggregate := aggregateDiagnostic(results, tasks)
	if aggregate.BehaviorTasks != 2 || aggregate.BehaviorPassed != 2 ||
		aggregate.BehaviorAccuracy != 1 {
		t.Fatalf("behavior aggregate = %+v", aggregate)
	}
	if aggregate.FailedTasks != 3 || aggregate.AutomaticallyAttributed != 1 ||
		aggregate.AttributionRate != 1.0/3 || aggregate.ProductFixableFailureRate != 1.0/3 ||
		aggregate.ManualReviewFailures != 1 || aggregate.UnattributedFailures != 1 ||
		aggregate.ModelOnlyFailures != 0 {
		t.Fatalf("failure aggregate = %+v", aggregate)
	}
	if aggregate.GovernanceTasks != 1 || aggregate.GovernancePassed != 1 ||
		aggregate.GovernancePassRate != 1 {
		t.Fatalf("governance aggregate = %+v", aggregate)
	}
}

func TestValidateDiagnosticTaskRequiresBehaviorAssertions(t *testing.T) {
	task := diagnosticTask{
		workloadTask: workloadTask{ID: "clarify", Prompt: "ambiguous prompt"},
		PromptLevel:  "ambiguous", ExpectedBehavior: "clarify",
		Dimensions: taskDimensions{
			Domain: []string{"commerce-core"}, Difficulty: "L3", Source: "expert_derived",
		},
	}
	if err := validateDiagnosticTask(task); err == nil ||
		!strings.Contains(err.Error(), "clarify_contains") {
		t.Fatalf("error = %v, want clarify_contains requirement", err)
	}
}

func TestWorkloadOraclesNonEmpty(t *testing.T) {
	oracles := workload.Oracles(workload.DefaultConfig())
	if len(oracles) < 8 {
		t.Fatalf("oracles = %d, want >= 8", len(oracles))
	}
}
