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

func TestMapConditions(t *testing.T) {
	t.Parallel()
	upper := func(c Condition) Condition {
		c.Field += "!"
		return c
	}
	exists := Exists{Relation: RelationRef{Name: "t"}, Predicate: Condition{Field: "c", Op: OpEq, Value: 3}}
	p := And{Preds: []Predicate{
		Condition{Field: "a", Op: OpEq, Value: 1},
		Or{Preds: []Predicate{Not{P: Condition{Field: "b", Op: OpIsNull}}, exists}},
	}}
	want := And{Preds: []Predicate{
		Condition{Field: "a!", Op: OpEq, Value: 1},
		Or{Preds: []Predicate{Not{P: Condition{Field: "b!", Op: OpIsNull}}, exists}},
	}}
	if got := MapConditions(p, upper); !reflect.DeepEqual(got, want) {
		t.Fatalf("MapConditions = %#v", got)
	}
	if MapConditions(nil, upper) != nil {
		t.Fatal("no predicate maps to none")
	}
	if p.Preds[0].(Condition).Field != "a" {
		t.Fatal("the input was changed")
	}
}
