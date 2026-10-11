package config

import (
	"fmt"

	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

// A row filter (a row policy, a tenant policy, the rows of a grant) is
// written as a JSON object: a condition {op, field, value}, or {and: [...]}
// or {or: [...]} of filters. ParseFilter is the one reading of that shape:
// validation and assembly both go through it, so what validation accepts is
// what runs.

// ParseFilter converts a declarative filter to a predicate. A nil filter is
// no predicate.
func ParseFilter(fc FilterConfig) (relalg.Predicate, error) {
	if fc == nil {
		return nil, nil
	}
	if op, ok := fc["op"]; ok {
		name, ok := op.(string)
		if !ok || !relalg.Op(name).Valid() {
			return nil, fmt.Errorf("invalid operator %q", op)
		}
		field, _ := fc["field"].(string)
		return relalg.Condition{Field: field, Op: relalg.Op(name), Value: fc["value"]}, nil
	}
	for _, key := range []string{"and", "or"} {
		raw, ok := fc[key]
		if !ok {
			continue
		}
		items, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf("%s must be a list", key)
		}
		preds := make([]relalg.Predicate, 0, len(items))
		for _, item := range items {
			child, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s item must be an object", key)
			}
			p, err := ParseFilter(child)
			if err != nil {
				return nil, err
			}
			preds = append(preds, p)
		}
		if key == "and" {
			return relalg.And{Preds: preds}, nil
		}
		return relalg.Or{Preds: preds}, nil
	}
	return nil, fmt.Errorf("missing op/and/or")
}

// FilterOf renders a predicate in the filter shape ParseFilter reads; nil
// for no predicate.
func FilterOf(p relalg.Predicate) FilterConfig {
	switch x := p.(type) {
	case relalg.Condition:
		return FilterConfig{"op": string(x.Op), "field": x.Field, "value": x.Value}
	case relalg.And:
		return FilterConfig{"and": filterList(x.Preds)}
	case relalg.Or:
		return FilterConfig{"or": filterList(x.Preds)}
	case relalg.Not:
		return FilterConfig{"not": FilterOf(x.P)}
	case relalg.IsNull:
		op := relalg.OpIsNull
		if x.Not {
			op = relalg.OpIsNotNull
		}
		return FilterConfig{"op": string(op), "field": x.Field}
	}
	return nil
}

func filterList(ps []relalg.Predicate) []any {
	out := make([]any, 0, len(ps))
	for _, p := range ps {
		out = append(out, FilterOf(p))
	}
	return out
}
