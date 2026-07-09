package relalg

import (
	"errors"
	"testing"
)

func TestOpValid(t *testing.T) {
	t.Parallel()
	valid := []Op{OpEq, OpNe, OpGt, OpGte, OpLt, OpLte, OpIn, OpNotIn, OpLike, OpIsNull, OpIsNotNull}
	for _, op := range valid {
		if !op.Valid() {
			t.Errorf("%q should be valid", op)
		}
	}
	for _, op := range []Op{"", "execute", "DROP TABLE t;--"} {
		if op.Valid() {
			t.Errorf("%q should be invalid", op)
		}
	}
}

func TestAggFuncValid(t *testing.T) {
	t.Parallel()
	for _, f := range []AggFunc{AggCount, AggSum, AggAvg, AggMin, AggMax} {
		if !f.Valid() {
			t.Errorf("%q should be valid", f)
		}
	}
	for _, f := range []AggFunc{"", "count(*)--", "median", "COUNT"} {
		if f.Valid() {
			t.Errorf("%q should be invalid", f)
		}
	}
}

func TestValidatePredicate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		pred    Predicate
		wantErr error
	}{
		{"nil", nil, nil},
		{"valid eq", Condition{Field: "id", Op: OpEq, Value: 1}, nil},
		{"valid in", Condition{Field: "id", Op: OpIn, Value: []any{1, 2}}, nil},
		{"invalid op", Condition{Field: "id", Op: "hack", Value: 1}, ErrInvalidOp},
		{"and with one bad", And{Preds: []Predicate{
			Condition{Field: "a", Op: OpEq, Value: 1},
			Condition{Field: "b", Op: "nope", Value: 2},
		}}, ErrInvalidOp},
		{"or nested valid", Or{Preds: []Predicate{
			Condition{Field: "a", Op: OpGt, Value: 0},
			Not{P: IsNull{Field: "b"}},
		}}, nil},
		{"isnull", IsNull{Field: "x"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidatePredicate(c.pred)
			if c.wantErr == nil {
				if err != nil {
					t.Fatalf("got %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("got %v, want %v", err, c.wantErr)
			}
		})
	}
}

func TestNodeConstruction(t *testing.T) {
	t.Parallel()
	// read_records shape: Limit(Sort(Project(Select(Scan, pred), items), order), n)
	expr := Limit{
		Count: 10,
		Input: Sort{
			OrderBy: []OrderTerm{{Field: "id", Dir: "asc"}},
			Input: Project{
				Items: []ProjectItem{{Field: "id"}, {Field: "name", Alias: "n"}},
				Input: Select{
					Predicate: And{Preds: []Predicate{
						Condition{Field: "id", Op: OpGt, Value: 0},
						IsNull{Field: "deleted_at"},
					}},
					Input: Scan{Relation: RelationRef{Name: "users"}},
				},
			},
		},
	}
	if err := ValidatePredicate(expr.Input.(Sort).Input.(Project).Input.(Select).Predicate); err != nil {
		t.Fatalf("validate: %v", err)
	}
}
