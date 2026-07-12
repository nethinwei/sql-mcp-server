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

func TestWorkloadOraclesNonEmpty(t *testing.T) {
	oracles := workload.Oracles(workload.DefaultConfig())
	if len(oracles) < 8 {
		t.Fatalf("oracles = %d, want >= 8", len(oracles))
	}
}
