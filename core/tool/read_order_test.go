package tool

import (
	"fmt"
	"math/big"
	"math/rand/v2"
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

// decimalKey writes a decimal as big.Rat.RatString does, so a number keys
// the same however it arrives.
func TestDecimalKeyMatchesBigRat(t *testing.T) {
	t.Parallel()
	inputs := []string{"0", "-0", "+0.0", "12.50", "12.5", "-12.500", "007", ".5", "5.", "0.000001",
		"999999999999999999", "123456789.123456789", "-0.0001", "1.", "+3", ".", "", "-", "1e5", "1.2.3", "a"}
	rng := rand.New(rand.NewPCG(5, 6))
	for range 2000 {
		inputs = append(inputs, fmt.Sprintf("%s%d.%0*d", []string{"", "-", "+"}[rng.IntN(3)], rng.Int64N(1e9),
			rng.IntN(9)+1, rng.Int64N(1e8)))
	}
	for _, in := range inputs {
		got, ok := decimalKey(in)
		r, valid := new(big.Rat).SetString(in)
		if ok && (!valid || got != "n:"+r.RatString()) {
			t.Fatalf("decimalKey(%q) = %q, big.Rat says %v %v", in, got, r, valid)
		}
	}
}
