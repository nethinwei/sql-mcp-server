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
	HighPrioritySemantic []string `yaml:"high_priority_semantic"`
	HighRiskGovernance   []string `yaml:"high_risk_governance"`
}

type taskFile struct {
	Tasks []Task `yaml:"tasks"`
}

type guidedMeta struct {
	Tasks map[string]Task `yaml:"tasks"`
}

const (
	minDiagnosticTasks = 35
	maxDiagnosticTasks = 50
)

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
	if err := validateReleaseGates(out, dimensions); err != nil {
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
	if err := validateVocabulary("semantic challenge", dimensions.SemanticChallenges); err != nil {
		return err
	}
	if err := validateVocabulary("governance challenge", dimensions.GovernanceChallenges); err != nil {
		return err
	}
	if err := validateValues("dimensions", "high-priority semantic challenge",
		dimensions.HighPrioritySemantic, semantic); err != nil {
		return err
	}
	if err := validateValues("dimensions", "high-risk governance challenge",
		dimensions.HighRiskGovernance, governance); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, task := range tasks {
		if task.ID == "" {
			return fmt.Errorf("task missing id")
		}
		if seen[task.ID] {
			return fmt.Errorf("duplicate task id %q", task.ID)
		}
		seen[task.ID] = true
		if !behaviors[task.ExpectedBehavior] {
			return fmt.Errorf("task %q: unknown expected behavior %q", task.ID, task.ExpectedBehavior)
		}
		if !levels[task.PromptLevel] {
			return fmt.Errorf("task %q: unknown prompt level %q", task.ID, task.PromptLevel)
		}
		if len(task.Dimensions.SemanticChallenges) == 0 &&
			len(task.Dimensions.GovernanceChallenges) == 0 {
			return fmt.Errorf("task %q: missing semantic or governance dimensions", task.ID)
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

func validateReleaseGates(tasks []Task, dimensions dimensionsFile) error {
	if len(tasks) < minDiagnosticTasks || len(tasks) > maxDiagnosticTasks {
		return fmt.Errorf("task count %d outside release range %d..%d",
			len(tasks), minDiagnosticTasks, maxDiagnosticTasks)
	}
	behaviorCount := map[string]int{}
	levelCount := map[string]int{}
	nonGuidedSemantic := map[string]bool{}
	negativeGovernance := map[string]bool{}
	for _, task := range tasks {
		behaviorCount[task.ExpectedBehavior]++
		levelCount[task.PromptLevel]++
		if task.PromptLevel != "guided" {
			for _, value := range task.Dimensions.SemanticChallenges {
				nonGuidedSemantic[value] = true
			}
		}
		if task.ExpectedBehavior == "deny" || task.Violation {
			for _, value := range task.Dimensions.GovernanceChallenges {
				negativeGovernance[value] = true
			}
		}
	}
	for _, behavior := range dimensions.ExpectedBehaviors {
		if behaviorCount[behavior] == 0 {
			return fmt.Errorf("expected behavior %q has no formal task", behavior)
		}
	}
	for _, level := range dimensions.PromptLevels {
		if levelCount[level] == 0 {
			return fmt.Errorf("prompt level %q has no formal task", level)
		}
	}
	for _, value := range dimensions.HighPrioritySemantic {
		if !nonGuidedSemantic[value] {
			return fmt.Errorf("high-priority semantic challenge %q has no non-guided task", value)
		}
	}
	for _, value := range dimensions.HighRiskGovernance {
		if !negativeGovernance[value] {
			return fmt.Errorf("high-risk governance challenge %q has no negative task", value)
		}
	}
	return nil
}

func validateVocabulary(kind string, values []string) error {
	if len(values) == 0 {
		return fmt.Errorf("%s vocabulary is empty", kind)
	}
	if len(stringSet(values)) != len(values) {
		return fmt.Errorf("%s vocabulary contains duplicates", kind)
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
