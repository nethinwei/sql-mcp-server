package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	workload "github.com/nethinwei/sql-mcp-server/fixtures/v4/generator"
)

// TestLoadWorkloadTasksAcceptsCurrentTaskSet locks the v4 task set to the
// loader contract and the design-doc distribution gate (§10.4): commerce +
// payments >= 12, ledger >= 5, live >= 3, total >= 20.
func TestLoadWorkloadTasksAcceptsCurrentTaskSet(t *testing.T) {
	file, err := loadWorkloadTasks("../../fixtures/v4/tasks/tasks.yaml",
		workload.Expected(workload.DefaultConfig()))
	if err != nil {
		t.Fatal(err)
	}
	byModule := map[string]int{}
	for _, task := range file.Tasks {
		byModule[task.Module]++
		if len(task.answerContains) == 0 {
			t.Fatalf("task %s: no injected answer expectation", task.ID)
		}
		if len(task.expectedRows) == 0 {
			t.Fatalf("task %s: no injected expected rows", task.ID)
		}
		if len(task.FailureCategories) == 0 {
			t.Fatalf("task %s: no candidate failure categories", task.ID)
		}
	}
	commercePayments := byModule["commerce-core"] + byModule["payment-orchestration"]
	if commercePayments < 12 {
		t.Fatalf("commerce+payments tasks = %d, want >= 12", commercePayments)
	}
	if byModule["ledger-settlement"] < 5 {
		t.Fatalf("ledger tasks = %d, want >= 5", byModule["ledger-settlement"])
	}
	if byModule["live-monetization"] < 3 {
		t.Fatalf("live tasks = %d, want >= 3", byModule["live-monetization"])
	}
	if len(file.Tasks) < 20 {
		t.Fatalf("total tasks = %d, want >= 20", len(file.Tasks))
	}
}

// TestLoadWorkloadTasksRejectsUnknownExpectation locks fail-closed loading:
// a task without a generator-computed expectation is a configuration error.
func TestLoadWorkloadTasksRejectsUnknownExpectation(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/tasks.yaml"
	content := "version: 4\ntasks:\n  - id: nope.unknown\n    module: commerce-core\n    prompt: x\n"
	if err := writeFile(path, content); err != nil {
		t.Fatal(err)
	}
	_, err := loadWorkloadTasks(path, workload.Expected(workload.DefaultConfig()))
	if err == nil || !strings.Contains(err.Error(), "no generator-computed expectation") {
		t.Fatalf("want expectation error, got %v", err)
	}
}

// TestAnswerHasValueNumericBoundaries locks the numeric matching semantics:
// small counts must not pass by substring accident.
func TestAnswerHasValueNumericBoundaries(t *testing.T) {
	cases := []struct {
		answer, want string
		match        bool
	}{
		{"there were 6 such intents", "6", true},
		{"there were 16 such intents", "6", false},
		{"6", "6", true},
		{"total 1,234.56 dollars", "1234.56", true},
		{"total 11234.56 dollars", "1234.56", false},
		{"revenue was 1234.567", "1234.56", false},
		{"the stripe channel had 13", "stripe", true},
		// Sentence punctuation after a number is a legitimate boundary
		// (2026-07-12 run-2 misjudgment: "**4,589**." failed to match 4589).
		{"the fee total is **4,589**.", "4589", true},
		{"was **$10.00**. Then it rose.", "10.00", true},
		{"it costs 10.00.5 whatever", "10.00", false},
		{"a value of 6.", "6", true},
		{"a value of 6.5", "6", false},
	}
	for _, c := range cases {
		if got := answerHasValue(normalizeAnswer(c.answer), c.want); got != c.match {
			t.Fatalf("answerHasValue(%q, %q) = %v, want %v", c.answer, c.want, got, c.match)
		}
	}
}

// TestGradeWorkloadAttribution locks the mechanical attribution rules.
func TestGradeWorkloadAttribution(t *testing.T) {
	task := workloadTask{
		ID: "t", Module: "commerce-core", ExpectTool: "aggregate_records",
		FailureCategories: []string{"grain", "time_semantics"},
		answerContains:    []string{"42"},
	}

	// No tool calls at all: discovery failure.
	r := gradeWorkload(task, transcript{FinalAnswer: "no idea"})
	if r.Passed || r.Attribution[0] != "agent_discovery" {
		t.Fatalf("no-calls attribution = %v", r.Attribution)
	}

	// Only governance denials: governance_policy.
	tr := transcript{Steps: []interactionStep{
		{Role: "tool", Tool: "aggregate_records", Denied: true, DenialCode: "COST_EXCEEDED"},
	}, FinalAnswer: "blocked"}
	r = gradeWorkload(task, tr)
	if r.Passed || r.Attribution[0] != "governance_policy" {
		t.Fatalf("governance attribution = %v", r.Attribution)
	}

	// Successful calls but wrong answer: falls back to declared candidates
	// flagged for manual review.
	tr = transcript{Steps: []interactionStep{
		{Role: "tool", Tool: "aggregate_records", Result: json.RawMessage(`{"count": 41}`)},
	}, FinalAnswer: "the count is 41"}
	r = gradeWorkload(task, tr)
	if r.Passed {
		t.Fatal("wrong answer must fail")
	}
	if r.AttributionConfidence != "manual_review" || r.Attribution[0] != "grain" {
		t.Fatalf("semantic attribution = %v (%s)", r.Attribution, r.AttributionConfidence)
	}

	// Correct answer passes and needs no attribution.
	tr = transcript{Steps: []interactionStep{
		{Role: "tool", Tool: "aggregate_records", Result: json.RawMessage(`{"count": 42}`)},
	}, FinalAnswer: "the count is 42"}
	r = gradeWorkload(task, tr)
	if !r.Passed || r.Attribution != nil {
		t.Fatalf("pass grading broken: passed=%v attribution=%v", r.Passed, r.Attribution)
	}
}

// TestEvidenceRowsFound locks the evidence-channel coverage counting.
func TestEvidenceRowsFound(t *testing.T) {
	rows := [][]string{{"stripe", "13"}, {"adyen", "7"}}
	calls := []interactionStep{
		{Role: "tool", Tool: "aggregate_records",
			Result: json.RawMessage(`[{"channel":"stripe","n":13},{"channel":"kcp","n":2}]`)},
	}
	if got := evidenceRowsFound(rows, calls); got != 1 {
		t.Fatalf("evidenceRowsFound = %d, want 1", got)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
