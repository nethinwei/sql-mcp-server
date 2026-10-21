package interp

import (
	"math"
	"math/big"
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

func TestCheck(t *testing.T) {
	t.Parallel()
	tenant := relalg.Condition{Field: "tenant_id", Op: relalg.OpEq, Value: int64(7)}
	open := relalg.Condition{Field: "status", Op: relalg.OpNe, Value: "archived"}
	owner := relalg.Condition{Field: "owner", Op: relalg.OpEq, Value: "ann"}
	cases := []struct {
		name     string
		p        relalg.Predicate
		known    map[string]any
		verdict  Verdict
		residual relalg.Predicate
	}{
		{"no predicate", nil, map[string]any{"a": 1}, Holds, nil},
		{"own tenant", tenant, map[string]any{"tenant_id": int64(7)}, Holds, nil},
		{"other tenant", tenant, map[string]any{"tenant_id": int64(8)}, Fails, nil},
		{"NULL tenant", tenant, map[string]any{"tenant_id": nil}, Fails, nil},
		{"unknown column", tenant, map[string]any{"name": "x"}, Depends, tenant},
		{"and drops decided", relalg.And{Preds: []relalg.Predicate{tenant, open}},
			map[string]any{"tenant_id": int64(7)}, Depends, open},
		{"and fails on one", relalg.And{Preds: []relalg.Predicate{tenant, open}},
			map[string]any{"tenant_id": int64(8)}, Fails, nil},
		{"or residual", relalg.Or{Preds: []relalg.Predicate{open, owner}},
			map[string]any{"status": "archived"}, Depends, owner},
		{"or holds", relalg.Or{Preds: []relalg.Predicate{open, owner}},
			map[string]any{"status": "new"}, Holds, nil},
		{"not of NULL is unknown", relalg.Not{P: tenant}, map[string]any{"tenant_id": nil}, Fails, nil},
		{"not residual", relalg.Not{P: owner}, map[string]any{}, Depends, relalg.Not{P: owner}},
		// A configuration's 0.1 is the decimal a column holds exactly.
		{"decimal", relalg.Condition{Field: "rate", Op: relalg.OpEq, Value: 0.1},
			map[string]any{"rate": big.NewRat(1, 10)}, Holds, nil},
		{"decimal order", relalg.Condition{Field: "rate", Op: relalg.OpLt, Value: 0.1},
			map[string]any{"rate": big.NewRat(1, 10)}, Fails, nil},
	}
	for _, c := range cases {
		residual, verdict, err := Check(c.p, c.known)
		if err != nil || verdict != c.verdict || !reflect.DeepEqual(residual, c.residual) {
			t.Errorf("%s: Check = %v, %v, %v; want %v, %v", c.name, residual, verdict, err, c.residual, c.verdict)
		}
	}
}

func TestCheckFailsClosed(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		p     relalg.Predicate
		known map[string]any
	}{
		"exists":       {relalg.Exists{Relation: relalg.RelationRef{Name: "t"}}, nil},
		"incomparable": {relalg.Condition{Field: "a", Op: relalg.OpEq, Value: int64(1)}, map[string]any{"a": "1"}},
		"NULL beside unknown": {relalg.Not{P: relalg.Or{Preds: []relalg.Predicate{
			relalg.Condition{Field: "a", Op: relalg.OpEq, Value: int64(1)},
			relalg.Condition{Field: "b", Op: relalg.OpEq, Value: int64(1)},
		}}}, map[string]any{"a": nil}},
		"NaN value": {relalg.Condition{Field: "a", Op: relalg.OpEq, Value: math.NaN()}, map[string]any{"a": int64(1)}},
		"NaN cell":  {relalg.Condition{Field: "a", Op: relalg.OpEq, Value: int64(1)}, map[string]any{"a": math.NaN()}},
		"infinite":  {relalg.Condition{Field: "a", Op: relalg.OpLt, Value: math.Inf(1)}, map[string]any{"a": 1.5}},
		"nil rat":   {relalg.Condition{Field: "a", Op: relalg.OpEq, Value: (*big.Rat)(nil)}, map[string]any{"a": 1.5}},
	} {
		if _, verdict, err := Check(c.p, c.known); verdict != Fails || err == nil {
			t.Errorf("%s: verdict %v, err %v; want Fails with a reason", name, verdict, err)
		}
	}
}

// TestCheckAgreesWithEval: for random predicates and rows, a decided verdict
// matches evaluating the whole row, and a residual evaluates as the whole
// predicate does.
func TestCheckAgreesWithEval(t *testing.T) {
	t.Parallel()
	cols := []string{"a", "b", "c"}
	rng := rand.New(rand.NewPCG(1, 2))
	for range 5000 {
		p := randomPredicate(rng, cols, 3)
		row := make([]any, len(cols))
		known := map[string]any{}
		for i, c := range cols {
			row[i] = randomValue(rng)
			if rng.IntN(2) == 0 {
				known[c] = row[i]
			}
		}
		rel := relation{Cols: cols}
		want, err := evalPredicate(rel, row, p)
		if err != nil {
			t.Fatal(err)
		}
		residual, verdict, err := Check(p, known)
		if err != nil {
			continue // undecidable: refused, which is always safe
		}
		switch verdict {
		case Holds:
			if want != triTrue {
				t.Fatalf("%#v on %v: Holds, eval %v", p, row, want)
			}
		case Fails:
			if want == triTrue {
				t.Fatalf("%#v on %v: Fails, eval TRUE", p, row)
			}
		case Depends:
			got, err := evalPredicate(rel, row, residual)
			if err != nil || (got == triTrue) != (want == triTrue) {
				t.Fatalf("%#v on %v: residual %#v eval %v, whole %v", p, row, residual, got, want)
			}
		}
	}
}

func randomPredicate(rng *rand.Rand, cols []string, depth int) relalg.Predicate {
	if depth == 0 || rng.IntN(3) == 0 {
		ops := []relalg.Op{relalg.OpEq, relalg.OpNe, relalg.OpLt, relalg.OpIsNull, relalg.OpIn}
		c := relalg.Condition{Field: cols[rng.IntN(len(cols))], Op: ops[rng.IntN(len(ops))]}
		switch c.Op {
		case relalg.OpIsNull:
		case relalg.OpIn:
			c.Value = []any{randomValue(rng), randomValue(rng)}
		default:
			c.Value = int64(rng.IntN(3))
		}
		return c
	}
	n := 1 + rng.IntN(3)
	preds := make([]relalg.Predicate, n)
	for i := range preds {
		preds[i] = randomPredicate(rng, cols, depth-1)
	}
	switch rng.IntN(3) {
	case 0:
		return relalg.And{Preds: preds}
	case 1:
		return relalg.Or{Preds: preds}
	default:
		return relalg.Not{P: preds[0]}
	}
}

func randomValue(rng *rand.Rand) any {
	if rng.IntN(4) == 0 {
		return nil
	}
	return int64(rng.IntN(3))
}
