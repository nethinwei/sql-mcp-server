package evalcoverage

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Task is a minimal view of a diagnostic task for coverage reporting.
type Task struct {
	ID               string `yaml:"id"`
	PromptLevel      string `yaml:"prompt_level"`
	ExpectedBehavior string `yaml:"expected_behavior"`
	Dimensions       struct {
		SemanticChallenges   []string `yaml:"semantic_challenges"`
		GovernanceChallenges []string `yaml:"governance_challenges"`
	} `yaml:"dimensions"`
	Violation bool `yaml:"violation"`
}

type dimensionsFile struct {
	SemanticChallenges   []string `yaml:"semantic_challenges"`
	GovernanceChallenges []string `yaml:"governance_challenges"`
	ExpectedBehaviors    []string `yaml:"expected_behaviors"`
	PromptLevels         []string `yaml:"prompt_levels"`
}

type taskFile struct {
	Tasks []Task `yaml:"tasks"`
}

type guidedMeta struct {
	Tasks map[string]Task `yaml:"tasks"`
}

// Matrix summarizes coverage counts per capability dimension.
type Matrix struct {
	Semantic    map[string]row `json:"semantic"`
	Governance  map[string]row `json:"governance"`
	Behaviors   map[string]int `json:"behaviors"`
	PromptLevel map[string]int `json:"promptLevel"`
}

type row struct {
	Positive int `json:"positive"`
	Clarify  int `json:"clarify"`
	Deny     int `json:"deny"`
	Natural  int `json:"natural"`
	Guided   int `json:"guided"`
}

// LoadTasks merges v4 guided metadata with v5 additions and validates tags.
func LoadTasks(v4MetaPath, additionsPath, dimensionsPath string) ([]Task, error) {
	metaData, err := os.ReadFile(v4MetaPath)
	if err != nil {
		return nil, err
	}
	var meta guidedMeta
	if err := yaml.Unmarshal(metaData, &meta); err != nil {
		return nil, err
	}
	addData, err := os.ReadFile(additionsPath)
	if err != nil {
		return nil, err
	}
	var add taskFile
	if err := yaml.Unmarshal(addData, &add); err != nil {
		return nil, err
	}
	var out []Task
	for id, t := range meta.Tasks {
		t.ID = id
		if t.PromptLevel == "" {
			t.PromptLevel = "guided"
		}
		if t.ExpectedBehavior == "" {
			t.ExpectedBehavior = "answer"
		}
		out = append(out, t)
	}
	out = append(out, add.Tasks...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	dimensions, err := loadDimensions(dimensionsPath)
	if err != nil {
		return nil, err
	}
	if err := validateTasks(out, dimensions); err != nil {
		return nil, err
	}
	return out, nil
}

func loadDimensions(path string) (dimensionsFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return dimensionsFile{}, err
	}
	var dimensions dimensionsFile
	if err := yaml.Unmarshal(data, &dimensions); err != nil {
		return dimensionsFile{}, err
	}
	return dimensions, nil
}

func validateTasks(tasks []Task, dimensions dimensionsFile) error {
	semantic := stringSet(dimensions.SemanticChallenges)
	governance := stringSet(dimensions.GovernanceChallenges)
	behaviors := stringSet(dimensions.ExpectedBehaviors)
	levels := stringSet(dimensions.PromptLevels)
	for _, task := range tasks {
		if !behaviors[task.ExpectedBehavior] {
			return fmt.Errorf("task %q: unknown expected behavior %q", task.ID, task.ExpectedBehavior)
		}
		if !levels[task.PromptLevel] {
			return fmt.Errorf("task %q: unknown prompt level %q", task.ID, task.PromptLevel)
		}
		if err := validateValues(task.ID, "semantic challenge",
			task.Dimensions.SemanticChallenges, semantic); err != nil {
			return err
		}
		if err := validateValues(task.ID, "governance challenge",
			task.Dimensions.GovernanceChallenges, governance); err != nil {
			return err
		}
	}
	return nil
}

func validateValues(taskID, kind string, values []string, allowed map[string]bool) error {
	for _, value := range values {
		if !allowed[value] {
			return fmt.Errorf("task %q: unknown %s %q", taskID, kind, value)
		}
	}
	return nil
}

func stringSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

// BuildMatrix computes coverage statistics from tasks.
func BuildMatrix(tasks []Task) Matrix {
	m := Matrix{
		Semantic:    map[string]row{},
		Governance:  map[string]row{},
		Behaviors:   map[string]int{},
		PromptLevel: map[string]int{},
	}
	for _, t := range tasks {
		m.Behaviors[t.ExpectedBehavior]++
		m.PromptLevel[t.PromptLevel]++
		for _, ch := range t.Dimensions.SemanticChallenges {
			r := m.Semantic[ch]
			updateRow(&r, t)
			m.Semantic[ch] = r
		}
		for _, ch := range t.Dimensions.GovernanceChallenges {
			r := m.Governance[ch]
			updateRow(&r, t)
			m.Governance[ch] = r
		}
	}
	return m
}

func updateRow(r *row, t Task) {
	switch t.ExpectedBehavior {
	case "clarify":
		r.Clarify++
	case "deny", "unsupported":
		r.Deny++
	default:
		r.Positive++
	}
	switch t.PromptLevel {
	case "guided":
		r.Guided++
	case "natural", "ambiguous":
		r.Natural++
	}
}

// Markdown renders the matrix as a markdown table.
func Markdown(m Matrix) string {
	var b strings.Builder
	b.WriteString("# Eval coverage matrix\n\n")
	b.WriteString("## Expected behaviors\n\n")
	keys := sortedKeys(m.Behaviors)
	for _, k := range keys {
		fmt.Fprintf(&b, "- %s: %d\n", k, m.Behaviors[k])
	}
	b.WriteString("\n## Prompt levels\n\n")
	for _, k := range sortedKeys(m.PromptLevel) {
		fmt.Fprintf(&b, "- %s: %d\n", k, m.PromptLevel[k])
	}
	b.WriteString("\n## Semantic challenges\n\n")
	b.WriteString("| Challenge | Positive | Clarify | Deny | Natural | Guided |\n")
	b.WriteString("| --- | ---: | ---: | ---: | ---: | ---: |\n")
	for _, k := range sortedKeysRow(m.Semantic) {
		r := m.Semantic[k]
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d |\n",
			k, r.Positive, r.Clarify, r.Deny, r.Natural, r.Guided)
	}
	b.WriteString("\n## Governance challenges\n\n")
	b.WriteString("| Challenge | Positive | Clarify | Deny | Natural | Guided |\n")
	b.WriteString("| --- | ---: | ---: | ---: | ---: | ---: |\n")
	for _, k := range sortedKeysRow(m.Governance) {
		r := m.Governance[k]
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d |\n",
			k, r.Positive, r.Clarify, r.Deny, r.Natural, r.Guided)
	}
	return b.String()
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedKeysRow(m map[string]row) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
