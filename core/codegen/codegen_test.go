package codegen

import (
	"errors"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/relalg"
	"github.com/nethinwei/sql-mcp-server/internal/testdialect"
)

func TestCompileSelectWithFilter(t *testing.T) {
	t.Parallel()
	r := NewRenderer(testdialect.Postgres{})
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
	r := NewRenderer(testdialect.Postgres{})
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
	r := NewRenderer(testdialect.MySQL{})
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
	if c.Kind != KindAggregate {
		t.Fatalf("kind = %q, want aggregate", c.Kind)
	}
}

func TestCompileInsertReturning(t *testing.T) {
	t.Parallel()
	ins := relalg.Insert{
		Target:  relalg.RelationRef{Name: "users"},
		Columns: []string{"name", "email"},
		Tuples:  []relalg.Tuple{{"alice", "a@x.com"}},
	}
	pg := NewRenderer(testdialect.Postgres{})
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
	if c.Kind != KindWrite {
		t.Fatalf("kind = %q, want write", c.Kind)
	}

	my := NewRenderer(testdialect.MySQL{})
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
	r := NewRenderer(testdialect.Postgres{})
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
	r := NewRenderer(testdialect.MySQL{})
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
	r := NewRenderer(testdialect.Postgres{})
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
	r := NewRenderer(testdialect.Postgres{})
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
	scoped := relalg.Select{
		Predicate: relalg.And{Preds: []relalg.Predicate{
			relalg.Condition{Field: "id", Op: relalg.OpEq, Value: 1},
			relalg.Condition{Field: "tenant_id", Op: relalg.OpEq, Value: 7},
		}},
		Input: relalg.Scan{Relation: relalg.RelationRef{Name: "users"}},
	}
	scopedCompiled, err := r.Compile(scoped, WithPrimaryKey("id"))
	if err != nil {
		t.Fatal(err)
	}
	if !scopedCompiled.IsPKPoint {
		t.Fatal("PK equality plus row policy remains a point lookup")
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
	r := NewRenderer(testdialect.Postgres{})
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

func TestCompileRejectsOversizedInList(t *testing.T) {
	t.Parallel()
	r := NewRenderer(testdialect.Postgres{})
	expr := relalg.Select{
		Predicate: relalg.Condition{Field: "id", Op: relalg.OpIn, Value: []any{1, 2, 3}},
		Input:     relalg.Scan{Relation: relalg.RelationRef{Name: "users"}},
	}
	if _, err := r.Compile(expr, WithMaxINCardinality(2)); !errors.Is(err, relalg.ErrINCardinality) {
		t.Fatalf("got %v, want ErrINCardinality", err)
	}
	if _, err := r.Compile(expr, WithMaxINCardinality(0)); !errors.Is(err, relalg.ErrINCardinality) {
		t.Fatalf("zero maximum got %v, want ErrINCardinality", err)
	}
}

func TestCompileDistinct(t *testing.T) {
	t.Parallel()
	r := NewRenderer(testdialect.Postgres{})
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
	r := NewRenderer(testdialect.Postgres{})
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
	if c.Kind != KindCall {
		t.Fatalf("kind = %q, want call", c.Kind)
	}
}

func TestCompileKinds(t *testing.T) {
	t.Parallel()
	r := NewRenderer(testdialect.Postgres{})
	read, err := r.Compile(relalg.Scan{Relation: relalg.RelationRef{Name: "users"}})
	if err != nil {
		t.Fatal(err)
	}
	write, err := r.Compile(relalg.Delete{Target: relalg.RelationRef{Name: "users"}})
	if err != nil {
		t.Fatal(err)
	}
	if read.Kind != KindRead || write.Kind != KindWrite {
		t.Fatalf("read kind=%q write kind=%q", read.Kind, write.Kind)
	}
}

func TestCompileInvalidOp(t *testing.T) {
	t.Parallel()
	r := NewRenderer(testdialect.Postgres{})
	expr := relalg.Select{
		Predicate: relalg.Condition{Field: "id", Op: "bad", Value: 1},
		Input:     relalg.Scan{Relation: relalg.RelationRef{Name: "users"}},
	}
	if _, err := r.Compile(expr); err == nil {
		t.Fatal("expected error for invalid operator")
	}
}

func TestCompileSortAndBoolean(t *testing.T) {
	t.Parallel()
	r := NewRenderer(testdialect.Postgres{})
	expr := relalg.Sort{
		OrderBy: []relalg.OrderTerm{{Field: "id", Dir: "desc"}},
		Input:   relalg.Scan{Relation: relalg.RelationRef{Name: "users"}},
	}
	c, err := r.Compile(expr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.SQL, "ORDER BY") || !strings.Contains(c.SQL, "DESC") {
		t.Fatalf("got %q", c.SQL)
	}
}

func TestCompileAndOrNotIsNull(t *testing.T) {
	t.Parallel()
	r := NewRenderer(testdialect.Postgres{})
	expr := relalg.Select{
		Predicate: relalg.And{Preds: []relalg.Predicate{
			relalg.Or{Preds: []relalg.Predicate{
				relalg.Condition{Field: "id", Op: relalg.OpGt, Value: 0},
				relalg.Not{P: relalg.IsNull{Field: "x"}},
			}},
			relalg.Condition{Field: "id", Op: relalg.OpIsNotNull, Value: nil},
		}},
		Input: relalg.Scan{Relation: relalg.RelationRef{Name: "users"}},
	}
	c, err := r.Compile(expr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.SQL, "AND") || !strings.Contains(c.SQL, "OR") || !strings.Contains(c.SQL, "IS NOT NULL") {
		t.Fatalf("got %q", c.SQL)
	}
}

func TestCompileIsPKPointRejectsOr(t *testing.T) {
	t.Parallel()
	r := NewRenderer(testdialect.Postgres{})
	// `id=5 OR name='x'` contains a PK equality but the OR branch can match
	// arbitrary rows; it must NOT be whitelisted as a point lookup.
	orExpr := relalg.Select{
		Predicate: relalg.Or{Preds: []relalg.Predicate{
			relalg.Condition{Field: "id", Op: relalg.OpEq, Value: 5},
			relalg.Condition{Field: "name", Op: relalg.OpEq, Value: "x"},
		}},
		Input: relalg.Scan{Relation: relalg.RelationRef{Name: "users"}},
	}
	c, err := r.Compile(orExpr, WithPrimaryKey("id"))
	if err != nil {
		t.Fatal(err)
	}
	if c.IsPKPoint {
		t.Fatal("OR predicate must not be a PK point lookup")
	}
	// `id=5 AND (a=1 OR b=2)`: the AND holds a full-PK equality but the nested
	// OR makes the whole predicate non-point.
	andOr := relalg.Select{
		Predicate: relalg.And{Preds: []relalg.Predicate{
			relalg.Condition{Field: "id", Op: relalg.OpEq, Value: 5},
			relalg.Or{Preds: []relalg.Predicate{
				relalg.Condition{Field: "a", Op: relalg.OpEq, Value: 1},
				relalg.Condition{Field: "b", Op: relalg.OpEq, Value: 2},
			}},
		}},
		Input: relalg.Scan{Relation: relalg.RelationRef{Name: "users"}},
	}
	c2, err := r.Compile(andOr, WithPrimaryKey("id"))
	if err != nil {
		t.Fatal(err)
	}
	if c2.IsPKPoint {
		t.Fatal("AND containing OR must not be a PK point lookup")
	}
}

func TestCompileRejectsInvalidAggFunc(t *testing.T) {
	t.Parallel()
	r := NewRenderer(testdialect.Postgres{})
	expr := relalg.Aggregate{
		Aggregates: []relalg.AggCall{{Func: "count(*) FROM t; DROP TABLE t--"}},
		Input:      relalg.Scan{Relation: relalg.RelationRef{Name: "users"}},
	}
	if _, err := r.Compile(expr); !errors.Is(err, relalg.ErrInvalidAggFunc) {
		t.Fatalf("got %v, want ErrInvalidAggFunc", err)
	}
}

func TestCompileWriteIsPKPoint(t *testing.T) {
	t.Parallel()
	r := NewRenderer(testdialect.Postgres{})
	upd := relalg.Update{
		Target:    relalg.RelationRef{Name: "users"},
		Predicate: relalg.Condition{Field: "id", Op: relalg.OpEq, Value: 1},
		Set:       []relalg.SetItem{{Field: "email", Value: "x"}},
	}
	c, err := r.Compile(upd, WithPrimaryKey("id"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.IsPKPoint {
		t.Fatal("UPDATE by full PK equality should be IsPKPoint")
	}
	del := relalg.Delete{
		Target:    relalg.RelationRef{Name: "users"},
		Predicate: relalg.Condition{Field: "status", Op: relalg.OpEq, Value: "a"},
	}
	c2, err := r.Compile(del, WithPrimaryKey("id"))
	if err != nil {
		t.Fatal(err)
	}
	if c2.IsPKPoint {
		t.Fatal("DELETE by a non-PK column must not be IsPKPoint")
	}
}
