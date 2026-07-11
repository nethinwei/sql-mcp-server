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
	ExpectDenialCode string   `yaml:"expect_denial_code"`
	ExpectRepair     bool     `yaml:"expect_repair"`
	Violation        bool     `yaml:"violation"`
}

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
	if file.MaxToolCalls <= 0 {
		file.MaxToolCalls = 8
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
