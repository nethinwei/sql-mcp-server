package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// taskSpec is one pilot task with its mechanical grading rules. See the
// header of eval/tasks.yaml for field semantics.
type taskSpec struct {
	ID               string   `yaml:"id"`
	Category         string   `yaml:"category"`
	Prompt           string   `yaml:"prompt"`
	ExpectTool       string   `yaml:"expect_tool"`
	AnswerContains   []string `yaml:"answer_contains"`
	AnswerAny        []string `yaml:"answer_any"`
	AnswerForbids    []string `yaml:"answer_forbids"`
	ForbidDecoys     []string `yaml:"forbid_decoys"`
	ExpectDenialCode string   `yaml:"expect_denial_code"`
	ExpectRepair     bool     `yaml:"expect_repair"`
	Violation        bool     `yaml:"violation"`
}

// Hard cost caps (roadmap v0.1.7): the task set and the per-task call budget
// may not grow past these without an explicit roadmap decision.
const (
	maxTaskCount   = 32 // 24 v2 tasks + at most 8 targeted v3 tasks
	maxToolCallCap = 8
)

type taskFile struct {
	Version      int        `yaml:"version"`
	MaxToolCalls int        `yaml:"max_tool_calls"`
	Tasks        []taskSpec `yaml:"tasks"`
}

func loadTasks(path string) (taskFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return taskFile{}, err
	}
	var file taskFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return taskFile{}, err
	}
	if len(file.Tasks) == 0 {
		return taskFile{}, fmt.Errorf("no tasks in %s", path)
	}
	if len(file.Tasks) > maxTaskCount {
		return taskFile{}, fmt.Errorf("%d tasks exceed the hard cap of %d", len(file.Tasks), maxTaskCount)
	}
	if file.MaxToolCalls <= 0 {
		file.MaxToolCalls = maxToolCallCap
	}
	if file.MaxToolCalls > maxToolCallCap {
		return taskFile{}, fmt.Errorf("max_tool_calls %d exceeds the hard cap of %d",
			file.MaxToolCalls, maxToolCallCap)
	}
	seen := map[string]bool{}
	for _, task := range file.Tasks {
		if task.ID == "" || task.Prompt == "" {
			return taskFile{}, fmt.Errorf("task %q: id and prompt are required", task.ID)
		}
		if seen[task.ID] {
			return taskFile{}, fmt.Errorf("duplicate task id %q", task.ID)
		}
		seen[task.ID] = true
	}
	return file, nil
}
