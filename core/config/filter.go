package config

import (
	"fmt"
	"maps"
	"slices"

	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

// A row filter (a row policy, a tenant policy, the rows of a grant) is
// written as a JSON object: a condition {op, field, value}, or {and: [...]},
// {or: [...]} or {not: {...}} of filters. Fields name columns of the table,
// exposed or not. ParseFilter is the one reading of that shape: validation
// and assembly both go through it, so what validation accepts is what runs.

// ParseFilter converts a declarative filter to a predicate. A nil filter is
// no predicate.
func ParseFilter(fc FilterConfig) (relalg.Predicate, error) {
	if fc == nil {
		return nil, nil
	}
	return parseFilter(fc)
}

func parseFilter(fc map[string]any) (relalg.Predicate, error) {
	if _, ok := fc["op"]; ok {
		return parseCondition(fc)
	}
	if len(fc) != 1 {
		return nil, fmt.Errorf("a filter is one of {op, field, value}, {and: [...]}, {or: [...]} or {not: {...}}, "+
			"got keys %v", slices.Sorted(maps.Keys(fc)))
	}
	if raw, ok := fc["not"]; ok {
		child, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("not must be a filter object")
		}
		p, err := parseFilter(child)
		if err != nil {
			return nil, err
		}
		return relalg.Not{P: p}, nil
	}
	for _, key := range []string{"and", "or"} {
		if raw, ok := fc[key]; ok {
			return parseGroup(key, raw)
		}
	}
	return nil, fmt.Errorf("missing op/and/or/not")
}

func parseGroup(key string, raw any) (relalg.Predicate, error) {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return nil, fmt.Errorf("%s must be a non-empty list", key)
	}
	preds := make([]relalg.Predicate, 0, len(items))
	for _, item := range items {
		child, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s item must be an object", key)
		}
		p, err := parseFilter(child)
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

func parseCondition(fc map[string]any) (relalg.Predicate, error) {
	for key := range fc {
		if key != "op" && key != "field" && key != "value" {
			return nil, fmt.Errorf("unknown key %q in a condition (op, field, value)", key)
		}
	}
	name, ok := fc["op"].(string)
	if !ok || !relalg.Op(name).Valid() {
		return nil, fmt.Errorf("invalid operator %q", fc["op"])
	}
	field, ok := fc["field"].(string)
	if !ok || field == "" {
		return nil, fmt.Errorf("condition %s needs a field", name)
	}
	c := relalg.Condition{Field: field, Op: relalg.Op(name), Value: fc["value"]}
	if err := checkConditionValue(c); err != nil {
		return nil, fmt.Errorf("field %q: %w", field, err)
	}
	return c, nil
}

// checkConditionValue checks a value as relalg.ValidatePredicate does, but
// admits a ${subject.x} placeholder wherever a value goes: the subject's
// attribute replaces it (a list, for in and not_in).
func checkConditionValue(c relalg.Condition) error {
	if s, ok := c.Value.(string); ok {
		if _, placeholder := rbac.SubjectPlaceholder(s); placeholder {
			if c.Op == relalg.OpIsNull || c.Op == relalg.OpIsNotNull {
				return fmt.Errorf("%s takes no value", c.Op)
			}
			return nil
		}
	}
	switch c.Op {
	case relalg.OpIsNull, relalg.OpIsNotNull:
	case relalg.OpIn, relalg.OpNotIn:
		if items, ok := c.Value.([]any); ok && slices.ContainsFunc(items, isComposite) {
			return fmt.Errorf("%s list items must be scalars", c.Op)
		}
	default:
		if c.Value == nil || isComposite(c.Value) {
			return fmt.Errorf("%s needs a scalar value", c.Op)
		}
	}
	return relalg.ValidatePredicate(c)
}

func isComposite(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return true
	}
	return false
}

// validateRowFilter parses a row filter of e. Its fields name columns, so
// one naming a field's alias (which the database does not know) is refused;
// that a column exists is checked against the table when the entity is
// assembled.
func validateRowFilter(e EntityConfig, fc FilterConfig) error {
	p, err := ParseFilter(fc)
	if err != nil {
		return err
	}
	columns := make(map[string]bool, len(e.Fields))
	for _, f := range e.Fields {
		columns[f.Name] = true
	}
	for _, field := range relalg.Fields(p) {
		if columns[field] {
			continue
		}
		for _, f := range e.Fields {
			if f.Alias == field {
				return fmt.Errorf("field %q is the alias of column %q: row filters name columns", field, f.Name)
			}
		}
	}
	return nil
}

// FilterOf renders a predicate in the filter shape ParseFilter reads; nil
// for no predicate (or one with an EXISTS, which filters cannot write).
func FilterOf(p relalg.Predicate) FilterConfig {
	if relalg.HasExists(p) {
		return nil
	}
	return filterOf(p)
}

func filterOf(p relalg.Predicate) FilterConfig {
	switch x := p.(type) {
	case relalg.Condition:
		fc := FilterConfig{"op": string(x.Op), "field": x.Field}
		if x.Op != relalg.OpIsNull && x.Op != relalg.OpIsNotNull {
			fc["value"] = x.Value
		}
		return fc
	case relalg.And:
		return FilterConfig{"and": filterList(x.Preds)}
	case relalg.Or:
		return FilterConfig{"or": filterList(x.Preds)}
	case relalg.Not:
		return FilterConfig{"not": map[string]any(filterOf(x.P))}
	}
	return nil
}

func filterList(ps []relalg.Predicate) []any {
	out := make([]any, 0, len(ps))
	for _, p := range ps {
		out = append(out, map[string]any(filterOf(p)))
	}
	return out
}
