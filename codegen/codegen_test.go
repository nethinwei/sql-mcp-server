package codegen

import (
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/dialect"
	"github.com/nethinwei/sql-mcp-server/relalg"
)

func TestCompileSelectWithFilter(t *testing.T) {
	t.Parallel()
	r := NewRenderer(dialect.Postgres{})
	expr := relalg.Select{
		Predicate: relalg.Condition{Field: "id", Op: relalg.OpEq, Value: int64(42)},
		Input:     relalg.Scan{Relation: relalg.RelationRef{Name: "users"}},
	}
	c, err := r.Compile(expr)
	if err != nil {
		t.Fatal(err)
	}
	want := `SELECT * FROM "users" WHERE "id" = $1`
	if c.SQL != want {
		t.Fatalf("got %q, want %q", c.SQL, want)
	}
	if len(c.Args) != 1 || c.Args[0] != int64(42) {
		t.Fatalf("args = %v", c.Args)
	}
	if !c.ReadOnly {
		t.Fatal("should be read-only")
	}
}

func TestCompileProjectAndLimit(t *testing.T) {
	t.Parallel()
	r := NewRenderer(dialect.Postgres{})
	expr := relalg.Limit{
		Count: 10, Offset: 5,
		Input: relalg.Project{
			Items: []relalg.ProjectItem{{Field: "id"}, {Field: "name", Alias: "n"}},
			Input: relalg.Scan{Relation: relalg.RelationRef{Name: "users"}},
		},
	}
	c, err := r.Compile(expr)
	if err != nil {
		t.Fatal(err)
	}
	want := `SELECT "id", "name" AS "n" FROM "users" LIMIT 10 OFFSET 5`
	if c.SQL != want {
		t.Fatalf("got %q, want %q", c.SQL, want)
	}
}

func TestCompileAggregate(t *testing.T) {
	t.Parallel()
	r := NewRenderer(dialect.MySQL{})
	expr := relalg.Aggregate{
		GroupBy:    []string{"dept"},
		Aggregates: []relalg.AggCall{{Func: "count"}, {Func: "sum", Field: "salary"}},
		Input:      relalg.Scan{Relation: relalg.RelationRef{Name: "employees"}},
	}
	c, err := r.Compile(expr)
	if err != nil {
		t.Fatal(err)
	}
	want := "SELECT `dept`, COUNT(*), SUM(`salary`) FROM `employees` GROUP BY `dept`"
	if c.SQL != want {
		t.Fatalf("got %q, want %q", c.SQL, want)
	}
}

func TestCompileInsertReturning(t *testing.T) {
	t.Parallel()
	ins := relalg.Insert{
		Target:  relalg.RelationRef{Name: "users"},
		Columns: []string{"name", "email"},
		Tuples:  []relalg.Tuple{{"alice", "a@x.com"}},
	}
	pg := NewRenderer(dialect.Postgres{})
	c, err := pg.Compile(ins, WithPrimaryKey("id"))
	if err != nil {
		t.Fatal(err)
	}
	want := `INSERT INTO "users" ("name", "email") VALUES ($1, $2) RETURNING "id"`
	if c.SQL != want {
		t.Fatalf("got %q, want %q", c.SQL, want)
	}
	if c.ReadOnly {
		t.Fatal("insert should not be read-only")
	}

	my := NewRenderer(dialect.MySQL{})
	c2, err := my.Compile(ins, WithPrimaryKey("id"))
	if err != nil {
		t.Fatal(err)
	}
	want2 := "INSERT INTO `users` (`name`, `email`) VALUES (?, ?)"
	if c2.SQL != want2 {
		t.Fatalf("got %q, want %q", c2.SQL, want2)
	}
}

func TestCompileUpdate(t *testing.T) {
	t.Parallel()
	r := NewRenderer(dialect.Postgres{})
	expr := relalg.Update{
		Target:    relalg.RelationRef{Name: "users"},
		Predicate: relalg.Condition{Field: "id", Op: relalg.OpEq, Value: int64(1)},
		Set:       []relalg.SetItem{{Field: "name", Value: "bob"}},
	}
	c, err := r.Compile(expr)
	if err != nil {
		t.Fatal(err)
	}
	want := `UPDATE "users" SET "name" = $1 WHERE "id" = $2`
	if c.SQL != want {
		t.Fatalf("got %q, want %q", c.SQL, want)
	}
	if len(c.AffectedTables) != 1 || c.AffectedTables[0] != "users" {
		t.Fatalf("affected = %v", c.AffectedTables)
	}
}

func TestCompileDelete(t *testing.T) {
	t.Parallel()
	r := NewRenderer(dialect.MySQL{})
	expr := relalg.Delete{
		Target:    relalg.RelationRef{Name: "users"},
		Predicate: relalg.Condition{Field: "id", Op: relalg.OpEq, Value: int64(1)},
	}
	c, err := r.Compile(expr)
	if err != nil {
		t.Fatal(err)
	}
	want := "DELETE FROM `users` WHERE `id` = ?"
	if c.SQL != want {
		t.Fatalf("got %q, want %q", c.SQL, want)
	}
}

func TestCompileInjectionAttempt(t *testing.T) {
	t.Parallel()
	r := NewRenderer(dialect.Postgres{})
	malicious := "1; DROP TABLE users;--"
	expr := relalg.Select{
		Predicate: relalg.Condition{Field: "name", Op: relalg.OpEq, Value: malicious},
		Input:     relalg.Scan{Relation: relalg.RelationRef{Name: "users"}},
	}
	c, err := r.Compile(expr)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToUpper(c.SQL), "DROP") {
		t.Fatalf("injection leaked into SQL: %q", c.SQL)
	}
	if c.Args[0] != malicious {
		t.Fatal("malicious value not preserved verbatim as a bound arg")
	}
}

func TestCompileIsPKPoint(t *testing.T) {
	t.Parallel()
	r := NewRenderer(dialect.Postgres{})
	pk := relalg.Select{
		Predicate: relalg.Condition{Field: "id", Op: relalg.OpEq, Value: 1},
		Input:     relalg.Scan{Relation: relalg.RelationRef{Name: "users"}},
	}
	c, err := r.Compile(pk, WithPrimaryKey("id"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.IsPKPoint {
		t.Fatal("expected IsPKPoint for full-PK equality")
	}

	nonPK := relalg.Select{
		Predicate: relalg.Condition{Field: "name", Op: relalg.OpEq, Value: "x"},
		Input:     relalg.Scan{Relation: relalg.RelationRef{Name: "users"}},
	}
	c2, err := r.Compile(nonPK, WithPrimaryKey("id"))
	if err != nil {
		t.Fatal(err)
	}
	if c2.IsPKPoint {
		t.Fatal("expected IsPKPoint false for non-PK filter")
	}
}

func TestCompileInList(t *testing.T) {
	t.Parallel()
	r := NewRenderer(dialect.Postgres{})
	expr := relalg.Select{
		Predicate: relalg.Condition{Field: "id", Op: relalg.OpIn, Value: []any{1, 2, 3}},
		Input:     relalg.Scan{Relation: relalg.RelationRef{Name: "users"}},
	}
	c, err := r.Compile(expr)
	if err != nil {
		t.Fatal(err)
	}
	want := `SELECT * FROM "users" WHERE "id" IN ($1, $2, $3)`
	if c.SQL != want {
		t.Fatalf("got %q, want %q", c.SQL, want)
	}
	if len(c.Args) != 3 {
		t.Fatalf("args = %v", c.Args)
	}
}

func TestCompileDistinct(t *testing.T) {
	t.Parallel()
	r := NewRenderer(dialect.Postgres{})
	c, err := r.Compile(relalg.Distinct{Input: relalg.Scan{Relation: relalg.RelationRef{Name: "users"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.SQL, "DISTINCT") {
		t.Fatalf("got %q", c.SQL)
	}
}

func TestCompileCall(t *testing.T) {
	t.Parallel()
	r := NewRenderer(dialect.Postgres{})
	c, err := r.Compile(relalg.Call{Procedure: relalg.RelationRef{Name: "sp"}, Args: []any{1, "x"}})
	if err != nil {
		t.Fatal(err)
	}
	want := `CALL "sp"($1, $2)`
	if c.SQL != want {
		t.Fatalf("got %q, want %q", c.SQL, want)
	}
	if len(c.Args) != 2 {
		t.Fatalf("args = %v", c.Args)
	}
}

func TestCompileInvalidOp(t *testing.T) {
	t.Parallel()
	r := NewRenderer(dialect.Postgres{})
	expr := relalg.Select{
		Predicate: relalg.Condition{Field: "id", Op: "bad", Value: 1},
		Input:     relalg.Scan{Relation: relalg.RelationRef{Name: "users"}},
	}
	if _, err := r.Compile(expr); err == nil {
		t.Fatal("expected error for invalid operator")
	}
}
