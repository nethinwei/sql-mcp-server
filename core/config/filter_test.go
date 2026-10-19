package config

import (
	"math"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

func TestParseFilterRejectsMalformedFilters(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		fc   FilterConfig
		want string
	}{
		"empty and":          {FilterConfig{"and": []any{}}, "non-empty"},
		"empty or":           {FilterConfig{"or": []any{}}, "non-empty"},
		"no field":           {FilterConfig{"op": "eq", "value": 1}, "needs a field"},
		"empty field":        {FilterConfig{"op": "eq", "field": "", "value": 1}, "needs a field"},
		"unknown key":        {FilterConfig{"op": "eq", "field": "a", "value": 1, "val": 2}, "unknown key"},
		"two groups":         {FilterConfig{"and": []any{}, "or": []any{}}, "one of"},
		"in without a list":  {FilterConfig{"op": "in", "field": "a", "value": 1}, "IN"},
		"empty in":           {FilterConfig{"op": "in", "field": "a", "value": []any{}}, "IN"},
		"in of objects":      {FilterConfig{"op": "in", "field": "a", "value": []any{map[string]any{}}}, "scalars"},
		"eq without value":   {FilterConfig{"op": "eq", "field": "a"}, "scalar value"},
		"eq of a list":       {FilterConfig{"op": "eq", "field": "a", "value": []any{1}}, "scalar value"},
		"is_null with value": {FilterConfig{"op": "is_null", "field": "a", "value": 1}, "value"},
		"not of a list":      {FilterConfig{"not": []any{}}, "filter object"},
		"unknown operator":   {FilterConfig{"op": "between", "field": "a"}, "invalid operator"},
		"eq NaN":             {FilterConfig{"op": "eq", "field": "a", "value": math.NaN()}, "finite"},
		"lt infinity":        {FilterConfig{"op": "lt", "field": "a", "value": math.Inf(-1)}, "finite"},
		"in with NaN":        {FilterConfig{"op": "in", "field": "a", "value": []any{1, math.NaN()}}, "finite"},
	} {
		if _, err := ParseFilter(c.fc); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}

func TestParseFilterAcceptsSubjectPlaceholders(t *testing.T) {
	t.Parallel()
	for _, fc := range []FilterConfig{
		{"op": "eq", "field": "tenant_id", "value": "${subject.tenant_id}"},
		{"op": "in", "field": "region", "value": "${subject.regions}"},
		{"not": map[string]any{"op": "is_null", "field": "deleted_at"}},
	} {
		if _, err := ParseFilter(fc); err != nil {
			t.Errorf("%v: %v", fc, err)
		}
	}
}

// TestFilterRoundTrip: what FilterOf writes, ParseFilter reads back.
func TestFilterRoundTrip(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(3, 4))
	for range 2000 {
		p := randomFilterPredicate(rng, 3)
		got, err := ParseFilter(FilterOf(p))
		if err != nil || !reflect.DeepEqual(got, p) {
			t.Fatalf("ParseFilter(FilterOf(%#v)) = %#v, %v", p, got, err)
		}
	}
}

func randomFilterPredicate(rng *rand.Rand, depth int) relalg.Predicate {
	if depth == 0 || rng.IntN(3) == 0 {
		field := []string{"a", "b", "c"}[rng.IntN(3)]
		switch rng.IntN(4) {
		case 0:
			return relalg.Condition{Field: field, Op: relalg.OpIsNotNull}
		case 1:
			return relalg.Condition{Field: field, Op: relalg.OpIn, Value: []any{"x", "y"}}
		case 2:
			return relalg.Condition{Field: field, Op: relalg.OpEq, Value: "${subject.tenant}"}
		default:
			return relalg.Condition{Field: field, Op: relalg.OpGt, Value: rng.IntN(10)}
		}
	}
	preds := make([]relalg.Predicate, 1+rng.IntN(3))
	for i := range preds {
		preds[i] = randomFilterPredicate(rng, depth-1)
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
