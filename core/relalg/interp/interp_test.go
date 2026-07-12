package interp

import (
	"errors"
	"math/big"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

// testDB is a small typed fixture exercising every boundary the spec names:
// NULLs, duplicates, negative numbers, empty matches.
func testDB() DB {
	return DB{
		"t": {
			Cols: []string{"id", "cat", "val"},
			Rows: [][]any{
				{int64(1), "a", int64(10)},
				{int64(2), "b", nil},
				{int64(3), nil, int64(-5)},
				{int64(4), "a", int64(10)},
				{int64(5), "b", int64(0)},
				{int64(6), nil, int64(7)},
			},
		},
	}
}

func scanT() relalg.Expr { return relalg.Scan{Relation: relalg.RelationRef{Name: "t"}} }

func mustEval(t *testing.T, e relalg.Expr) Result {
	t.Helper()
	res, err := Eval(testDB(), e)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func ids(res Result) []int64 {
	out := make([]int64, len(res.Rows))
	for i, row := range res.Rows {
		out[i] = row[0].(int64)
	}
	return out
}

func wantIDs(t *testing.T, res Result, want ...int64) {
	t.Helper()
	got := ids(res)
	if len(got) != len(want) {
		t.Fatalf("got rows %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got rows %v, want %v", got, want)
		}
	}
}

func TestComparisonWithNullIsUnknown(t *testing.T) {
	// val != 10 must NOT include the NULL row (UNKNOWN filtered).
	res := mustEval(t, relalg.Select{Input: scanT(),
		Predicate: relalg.Condition{Field: "val", Op: relalg.OpNe, Value: int64(10)}})
	wantIDs(t, res, 3, 5, 6)
}

func TestNotOfUnknownStaysUnknown(t *testing.T) {
	// NOT(val > 0): NULL row stays filtered, not resurrected by NOT.
	res := mustEval(t, relalg.Select{Input: scanT(),
		Predicate: relalg.Not{P: relalg.Condition{Field: "val", Op: relalg.OpGt, Value: int64(0)}}})
	wantIDs(t, res, 3, 5)
}

func TestInWithNullInList(t *testing.T) {
	// val IN (10, NULL): only TRUE rows pass; non-matching rows are UNKNOWN.
	res := mustEval(t, relalg.Select{Input: scanT(),
		Predicate: relalg.Condition{Field: "val", Op: relalg.OpIn, Value: []any{int64(10), nil}}})
	wantIDs(t, res, 1, 4)
}

func TestNotInWithNullInListMatchesNothing(t *testing.T) {
	res := mustEval(t, relalg.Select{Input: scanT(),
		Predicate: relalg.Condition{Field: "val", Op: relalg.OpNotIn, Value: []any{int64(10), nil}}})
	wantIDs(t, res)
}

func TestIsNullIsTwoValued(t *testing.T) {
	res := mustEval(t, relalg.Select{Input: scanT(),
		Predicate: relalg.Condition{Field: "cat", Op: relalg.OpIsNull}})
	wantIDs(t, res, 3, 6)
	res = mustEval(t, relalg.Select{Input: scanT(),
		Predicate: relalg.IsNull{Field: "cat", Not: true}})
	wantIDs(t, res, 1, 2, 4, 5)
}

func TestAndOrThreeValued(t *testing.T) {
	// (val > 0 OR cat = 'b'): row 2 (val NULL, cat b) is TRUE via OR.
	res := mustEval(t, relalg.Select{Input: scanT(), Predicate: relalg.Or{Preds: []relalg.Predicate{
		relalg.Condition{Field: "val", Op: relalg.OpGt, Value: int64(0)},
		relalg.Condition{Field: "cat", Op: relalg.OpEq, Value: "b"},
	}}})
	wantIDs(t, res, 1, 2, 4, 5, 6)
	// (val >= 0 AND cat = 'b'): row 2 is UNKNOWN AND TRUE = UNKNOWN -> filtered.
	res = mustEval(t, relalg.Select{Input: scanT(), Predicate: relalg.And{Preds: []relalg.Predicate{
		relalg.Condition{Field: "val", Op: relalg.OpGte, Value: int64(0)},
		relalg.Condition{Field: "cat", Op: relalg.OpEq, Value: "b"},
	}}})
	wantIDs(t, res, 5)
}

func TestLikeSemantics(t *testing.T) {
	db := DB{"s": {Cols: []string{"id", "name"}, Rows: [][]any{
		{int64(1), "alpha"}, {int64(2), "beta"}, {int64(3), nil}, {int64(4), "al_ha"},
	}}}
	scan := relalg.Scan{Relation: relalg.RelationRef{Name: "s"}}
	res, err := Eval(db, relalg.Select{Input: scan,
		Predicate: relalg.Condition{Field: "name", Op: relalg.OpLike, Value: "al%"}})
	if err != nil {
		t.Fatal(err)
	}
	wantIDs(t, Result(res), 1, 4)
	// _ matches exactly one character; literal regex metacharacters are inert.
	res, err = Eval(db, relalg.Select{Input: scan,
		Predicate: relalg.Condition{Field: "name", Op: relalg.OpLike, Value: "al_ha"}})
	if err != nil {
		t.Fatal(err)
	}
	wantIDs(t, res, 1, 4)
	// Case-sensitive canonical semantics.
	res, err = Eval(db, relalg.Select{Input: scan,
		Predicate: relalg.Condition{Field: "name", Op: relalg.OpLike, Value: "AL%"}})
	if err != nil {
		t.Fatal(err)
	}
	wantIDs(t, res)
}

func TestBagSemanticsAndDistinct(t *testing.T) {
	proj := relalg.Project{Input: scanT(), Items: []relalg.ProjectItem{{Field: "cat"}}}
	res := mustEval(t, proj)
	if len(res.Rows) != 6 {
		t.Fatalf("projection must keep duplicates, got %d rows", len(res.Rows))
	}
	distinct := mustEval(t, relalg.Distinct{Input: proj})
	// a, b, NULL: NULLs collapse in DISTINCT.
	if len(distinct.Rows) != 3 {
		t.Fatalf("distinct over (a,b,NULL,a,b,NULL) must be 3 rows, got %d", len(distinct.Rows))
	}
}

func TestSortLimitOffset(t *testing.T) {
	sorted := relalg.Sort{
		Input:   relalg.Select{Input: scanT(), Predicate: relalg.IsNull{Field: "val", Not: true}},
		OrderBy: []relalg.OrderTerm{{Field: "val", Dir: "desc"}, {Field: "id", Dir: "asc"}},
	}
	res := mustEval(t, sorted)
	wantIDs(t, res, 1, 4, 6, 5, 3)
	res = mustEval(t, relalg.Limit{Input: sorted, Count: 2, Offset: 1})
	wantIDs(t, res, 4, 6)
	res = mustEval(t, relalg.Limit{Input: sorted, Count: 10, Offset: 4})
	wantIDs(t, res, 3)
	res = mustEval(t, relalg.Limit{Input: sorted, Count: 2, Offset: 99})
	wantIDs(t, res)
}

func globalAgg(pred relalg.Predicate, calls ...relalg.AggCall) relalg.Expr {
	in := scanT()
	if pred != nil {
		in = relalg.Select{Input: scanT(), Predicate: pred}
	}
	return relalg.Aggregate{Input: in, Aggregates: calls}
}

func TestCountStarVsCountField(t *testing.T) {
	res := mustEval(t, globalAgg(nil,
		relalg.AggCall{Func: relalg.AggCount},
		relalg.AggCall{Func: relalg.AggCount, Field: "val"},
	))
	if res.Rows[0][0] != int64(6) || res.Rows[0][1] != int64(5) {
		t.Fatalf("count(*)=%v count(val)=%v, want 6 and 5", res.Rows[0][0], res.Rows[0][1])
	}
}

func TestAggregatesSkipNulls(t *testing.T) {
	res := mustEval(t, globalAgg(nil,
		relalg.AggCall{Func: relalg.AggSum, Field: "val"},
		relalg.AggCall{Func: relalg.AggMin, Field: "val"},
		relalg.AggCall{Func: relalg.AggMax, Field: "val"},
		relalg.AggCall{Func: relalg.AggAvg, Field: "val"},
	))
	row := res.Rows[0]
	if row[0].(*big.Rat).Cmp(big.NewRat(22, 1)) != 0 {
		t.Fatalf("sum = %v, want 22", row[0])
	}
	if row[1].(int64) != -5 || row[2].(int64) != 10 {
		t.Fatalf("min/max = %v/%v, want -5/10", row[1], row[2])
	}
	if row[3].(*big.Rat).Cmp(big.NewRat(22, 5)) != 0 {
		t.Fatalf("avg = %v, want 22/5", row[3])
	}
}

func TestGlobalAggregateOverEmptyInput(t *testing.T) {
	pred := relalg.Condition{Field: "id", Op: relalg.OpGt, Value: int64(100)}
	res := mustEval(t, globalAgg(pred,
		relalg.AggCall{Func: relalg.AggCount},
		relalg.AggCall{Func: relalg.AggSum, Field: "val"},
		relalg.AggCall{Func: relalg.AggMax, Field: "val"},
	))
	if len(res.Rows) != 1 {
		t.Fatalf("global aggregate over empty input must yield exactly 1 row, got %d", len(res.Rows))
	}
	row := res.Rows[0]
	if row[0] != int64(0) || row[1] != nil || row[2] != nil {
		t.Fatalf("empty input: count=%v sum=%v max=%v, want 0/NULL/NULL", row[0], row[1], row[2])
	}
}

func TestGroupedAggregateOverEmptyInputYieldsZeroRows(t *testing.T) {
	pred := relalg.Condition{Field: "id", Op: relalg.OpGt, Value: int64(100)}
	res := mustEval(t, relalg.Aggregate{
		Input:      relalg.Select{Input: scanT(), Predicate: pred},
		GroupBy:    []string{"cat"},
		Aggregates: []relalg.AggCall{{Func: relalg.AggCount}},
	})
	if len(res.Rows) != 0 {
		t.Fatalf("grouped aggregate over empty input must yield 0 rows, got %d", len(res.Rows))
	}
}

func TestGroupByNullFormsOneGroup(t *testing.T) {
	res := mustEval(t, relalg.Aggregate{
		Input:      scanT(),
		GroupBy:    []string{"cat"},
		Aggregates: []relalg.AggCall{{Func: relalg.AggCount}},
	})
	if len(res.Rows) != 3 {
		t.Fatalf("groups over (a,b,NULL) must be 3, got %d", len(res.Rows))
	}
	counts := map[string]int64{}
	for _, row := range res.Rows {
		key := "NULL"
		if row[0] != nil {
			key = row[0].(string)
		}
		counts[key] = row[1].(int64)
	}
	if counts["a"] != 2 || counts["b"] != 2 || counts["NULL"] != 2 {
		t.Fatalf("group counts = %v, want a:2 b:2 NULL:2", counts)
	}
}

func TestCountDistinct(t *testing.T) {
	res := mustEval(t, globalAgg(nil,
		relalg.AggCall{Func: relalg.AggCount, Field: "val", Distinct: true}))
	if res.Rows[0][0] != int64(4) {
		t.Fatalf("count(distinct val) = %v, want 4 (10,-5,0,7)", res.Rows[0][0])
	}
}

func TestInvalidIRRejected(t *testing.T) {
	_, err := Eval(testDB(), relalg.Select{Input: scanT(),
		Predicate: relalg.Condition{Field: "val", Op: relalg.Op("drop"), Value: 1}})
	if !errors.Is(err, relalg.ErrInvalidOp) {
		t.Fatalf("invalid operator must be rejected, got %v", err)
	}
	_, err = Eval(testDB(), relalg.Project{Input: scanT(),
		Items: []relalg.ProjectItem{{Field: "nope"}}})
	if !errors.Is(err, ErrUnknownColumn) {
		t.Fatalf("unknown column must be rejected, got %v", err)
	}
	_, err = Eval(testDB(), relalg.Insert{Target: relalg.RelationRef{Name: "t"}})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("write expressions must be unsupported, got %v", err)
	}
}
