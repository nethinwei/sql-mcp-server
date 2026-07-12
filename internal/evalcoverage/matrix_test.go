package evalcoverage

import (
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
		SemanticChallenges: []string{"grain"},
		ExpectedBehaviors:  []string{"answer"},
		PromptLevels:       []string{"natural"},
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
