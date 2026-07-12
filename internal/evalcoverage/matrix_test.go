package evalcoverage

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestValidateTasksRejectsUnknownDimension(t *testing.T) {
	task := Task{
		ID:               "task-1",
		PromptLevel:      "natural",
		ExpectedBehavior: "answer",
	}
	task.Dimensions.SemanticChallenges = []string{"typo"}
	dimensions := dimensionsFile{
		SemanticChallenges:   []string{"grain"},
		GovernanceChallenges: []string{"masking"},
		ExpectedBehaviors:    []string{"answer"},
		PromptLevels:         []string{"natural"},
	}
	err := validateTasks([]Task{task}, dimensions)
	if err == nil || !strings.Contains(err.Error(), `unknown semantic challenge "typo"`) {
		t.Fatalf("error = %v, want unknown semantic challenge", err)
	}
}

func TestValidateTasksAcceptsDeclaredTags(t *testing.T) {
	task := Task{
		ID:               "task-1",
		PromptLevel:      "guided",
		ExpectedBehavior: "deny",
	}
	task.Dimensions.SemanticChallenges = []string{"grain"}
	task.Dimensions.GovernanceChallenges = []string{"masking"}
	dimensions := dimensionsFile{
		SemanticChallenges:   []string{"grain"},
		GovernanceChallenges: []string{"masking"},
		ExpectedBehaviors:    []string{"deny"},
		PromptLevels:         []string{"guided"},
	}
	if err := validateTasks([]Task{task}, dimensions); err != nil {
		t.Fatal(err)
	}
}

func TestLoadTasksValidatesRepositoryTaskSet(t *testing.T) {
	tasks, err := LoadTasks(
		"../../fixtures/v4/tasks-v5/guided-metadata.yaml",
		"../../fixtures/v4/tasks-v5/additions.yaml",
		"../../fixtures/v4/coverage/dimensions.yaml",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 48 {
		t.Fatalf("task count = %d, want 48", len(tasks))
	}
	for _, task := range tasks {
		if task.ID == "" || task.PromptLevel == "" || task.ExpectedBehavior == "" {
			t.Fatalf("task has missing decoded fields: %+v", task)
		}
	}
}

func TestValidateTasksRejectsDuplicateID(t *testing.T) {
	task := Task{ID: "duplicate", PromptLevel: "natural", ExpectedBehavior: "answer"}
	task.Dimensions.SemanticChallenges = []string{"grain"}
	dimensions := dimensionsFile{
		SemanticChallenges:   []string{"grain"},
		GovernanceChallenges: []string{"masking"},
		ExpectedBehaviors:    []string{"answer"},
		PromptLevels:         []string{"natural"},
	}
	if err := validateTasks([]Task{task, task}, dimensions); err == nil ||
		!strings.Contains(err.Error(), "duplicate task id") {
		t.Fatalf("error = %v, want duplicate task id", err)
	}
}

func TestValidateReleaseGatesRequireDiagnosticCoverage(t *testing.T) {
	dimensions := dimensionsFile{
		ExpectedBehaviors:    []string{"answer", "clarify", "deny", "qualify", "unsupported"},
		PromptLevels:         []string{"guided", "natural", "ambiguous"},
		HighPrioritySemantic: []string{"grain"},
		HighRiskGovernance:   []string{"masking"},
		SemanticChallenges:   []string{"grain"},
		GovernanceChallenges: []string{"masking"},
	}
	tasks := make([]Task, minDiagnosticTasks)
	for i := range tasks {
		tasks[i] = Task{
			ID:               "answer",
			PromptLevel:      "guided",
			ExpectedBehavior: "answer",
		}
	}
	if err := validateReleaseGates(tasks, dimensions); err == nil ||
		!strings.Contains(err.Error(), `expected behavior "clarify"`) {
		t.Fatalf("error = %v, want missing behavior", err)
	}
}

func TestBuildMatrixCountsTaskKinds(t *testing.T) {
	answer := Task{ID: "answer", PromptLevel: "natural", ExpectedBehavior: "answer"}
	answer.Dimensions.SemanticChallenges = []string{"grain"}
	deny := Task{ID: "deny", PromptLevel: "guided", ExpectedBehavior: "deny"}
	deny.Dimensions.GovernanceChallenges = []string{"masking"}

	matrix := BuildMatrix([]Task{answer, deny})
	if matrix.Behaviors["answer"] != 1 || matrix.Behaviors["deny"] != 1 {
		t.Fatalf("behaviors = %#v", matrix.Behaviors)
	}
	if matrix.Semantic["grain"].Natural != 1 || matrix.Semantic["grain"].Positive != 1 {
		t.Fatalf("semantic grain = %#v", matrix.Semantic["grain"])
	}
	if matrix.Governance["masking"].Guided != 1 || matrix.Governance["masking"].Deny != 1 {
		t.Fatalf("governance masking = %#v", matrix.Governance["masking"])
	}
}

func TestCoverageArtifactsMatchTaskSet(t *testing.T) {
	tasks, err := LoadTasks(
		"../../fixtures/v4/tasks-v5/guided-metadata.yaml",
		"../../fixtures/v4/tasks-v5/additions.yaml",
		"../../fixtures/v4/coverage/dimensions.yaml",
	)
	if err != nil {
		t.Fatal(err)
	}
	matrix := BuildMatrix(tasks)
	jsonData, err := json.MarshalIndent(matrix, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, "../../fixtures/v4/coverage/matrix.json", string(jsonData))
	assertFileContent(t, "../../fixtures/v4/coverage/matrix.md", Markdown(matrix))
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s is stale; run make eval-coverage", path)
	}
}
