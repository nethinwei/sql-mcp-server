package tool

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/entity"
)

// The columns an expansion joins on are projected in one order, so the same
// call compiles to the same SQL.
func TestExpandProjectsJoinColumnsInOrder(t *testing.T) {
	t.Parallel()
	e := entity.Entity{Name: "orders", Relations: []entity.Relationship{{
		Name: "lines", Target: "lines", JoinOn: map[string]string{"c": "z", "a": "x", "d": "w", "b": "y"},
	}}}
	in := readInput{Fields: []string{"id"}, Expand: []string{"lines"}}
	for range 20 {
		_, projection, err := readFieldNames(entity.Resolved{Entity: e}, in)
		if err != nil || !slices.Equal(projection, []string{"id", "a", "b", "c", "d"}) {
			t.Fatalf("projection = %v, %v", projection, err)
		}
		if got := expandJoinColumns(e, in.Expand); !slices.Equal(got, []string{"a", "b", "c", "d"}) {
			t.Fatalf("join columns = %v", got)
		}
	}
}

// The rules on fields are applied through fieldUses alone: a path naming
// fields says how it uses them, and gets every rule of that use (I15, I16).
func TestFieldRulesOnlyThroughFieldUses(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	rule := regexp.MustCompile(`\b(validateFields|validateUnmaskedFields|validateWritable|distinctColumns)\(`)
	allowed := map[string]bool{"field_uses.go": true}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") || allowed[file] {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if m := rule.FindString(line); m != "" && !strings.HasPrefix(line, "func ") &&
				!strings.Contains(line, "return validateUnmaskedFields(res, fields...) != nil") {
				t.Errorf("%s: %s applied outside fieldUses: %s", file, m, strings.TrimSpace(line))
			}
		}
	}
}
