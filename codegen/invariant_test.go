package codegen

import (
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/internal/testdialect"
	"github.com/nethinwei/sql-mcp-server/relalg"
)

// TestInvariantNoInjection verifies core invariant I3: user values are always
// bound to placeholders, never string-interpolated into SQL. It sweeps a set of
// classic injection vectors; each must appear only in args, never in the SQL.
func TestInvariantNoInjection(t *testing.T) {
	t.Parallel()
	r := NewRenderer(testdialect.Postgres{})
	vectors := []string{
		"1; DROP TABLE users;--",
		"' OR '1'='1",
		"/* */",
		"x' OR 'x'='x",
		"\\",
		"'; --",
	}
	for _, v := range vectors {
		expr := relalg.Select{
			Predicate: relalg.Condition{Field: "name", Op: relalg.OpEq, Value: v},
			Input:     relalg.Scan{Relation: relalg.RelationRef{Name: "users"}},
		}
		c, err := r.Compile(expr)
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		if strings.Contains(c.SQL, v) {
			t.Fatalf("injection vector %q leaked into SQL %q", v, c.SQL)
		}
		if len(c.Args) != 1 || c.Args[0] != v {
			t.Fatalf("value not bound verbatim as arg: %v", c.Args)
		}
	}
}

// TestInvariantNoInjectionMySQL repeats I3 on the MySQL dialect (?
// placeholders) to ensure both renderers parameterize.
func TestInvariantNoInjectionMySQL(t *testing.T) {
	t.Parallel()
	r := NewRenderer(testdialect.MySQL{})
	v := "' OR '1'='1"
	expr := relalg.Select{
		Predicate: relalg.Condition{Field: "name", Op: relalg.OpEq, Value: v},
		Input:     relalg.Scan{Relation: relalg.RelationRef{Name: "users"}},
	}
	c, err := r.Compile(expr)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(c.SQL, v) {
		t.Fatalf("value leaked into MySQL SQL %q", c.SQL)
	}
	if len(c.Args) != 1 || c.Args[0] != v {
		t.Fatalf("not bound: %v", c.Args)
	}
}
