package relalg

import (
	"errors"
	"testing"
)

func TestAdversarialPredicateCorpus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		pred Predicate
	}{
		{"empty field", Condition{Op: OpEq, Value: 1}},
		{"value on is null", Condition{Field: "id", Op: OpIsNull, Value: "confused"}},
		{"empty and", And{}},
		{"nil and operand", And{Preds: []Predicate{nil}}},
		{"empty or", Or{}},
		{"nil not operand", Not{}},
		{"empty is-null field", IsNull{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidatePredicate(test.pred); !errors.Is(err, ErrInvalidPredicate) {
				t.Fatalf("error = %v, want ErrInvalidPredicate", err)
			}
		})
	}
}

func FuzzValidatePredicate(f *testing.F) {
	f.Add("eq", "id", uint8(1), uint8(0))
	f.Add("in", "tenant_id", uint8(2), uint8(1))
	f.Add("DROP TABLE", "id", uint8(0), uint8(2))
	f.Add("is_null", "", uint8(0), uint8(3))

	f.Fuzz(func(t *testing.T, opText, field string, cardinality, shape uint8) {
		values := make([]any, int(cardinality%5))
		for i := range values {
			values[i] = int64(i)
		}
		var value any = int64(1)
		op := Op(opText)
		if op == OpIn || op == OpNotIn {
			value = values
		}
		if op == OpIsNull || op == OpIsNotNull {
			value = nil
		}
		leaf := Condition{Field: field, Op: op, Value: value}
		var pred Predicate = leaf
		switch shape % 4 {
		case 1:
			pred = And{Preds: []Predicate{leaf}}
		case 2:
			pred = Or{Preds: []Predicate{leaf}}
		case 3:
			pred = Not{P: leaf}
		}

		err := ValidatePredicateWithMaxINCardinality(pred, 4)
		wantValid := field != "" && op.Valid()
		if op == OpIn || op == OpNotIn {
			wantValid = wantValid && len(values) > 0 && len(values) <= 4
		}
		if wantValid && err != nil {
			t.Fatalf("valid predicate rejected: %v", err)
		}
		if !wantValid && err == nil {
			t.Fatalf("invalid predicate accepted: %#v", pred)
		}
	})
}
