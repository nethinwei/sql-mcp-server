package relalg

import (
	"reflect"
	"testing"
)

func TestFields(t *testing.T) {
	t.Parallel()
	p := And{Preds: []Predicate{
		Condition{Field: "a", Op: OpEq, Value: 1},
		Or{Preds: []Predicate{Condition{Field: "b", Op: OpIsNull}, Not{P: Condition{Field: "a", Op: OpEq, Value: 2}}}},
		Exists{Relation: RelationRef{Name: "t"}, Predicate: Condition{Field: "c", Op: OpEq, Value: 3}},
	}}
	if got := Fields(p); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("Fields = %v", got)
	}
	if got := Fields(nil); got != nil {
		t.Fatalf("Fields(nil) = %v", got)
	}
}
