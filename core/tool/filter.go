package tool

import (
	"fmt"
	"slices"

	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

// A filter is a list of conditions, all of which hold: each a condition on
// one field, or a group (any: one holds, all: every one holds, not: it does
// not hold) of them.

// filterToPredicate validates a filter (at most maxConds conditions when
// positive, counted through its groups) and converts it to a conjunction.
func filterToPredicate(conds []condJSON, maxConds int) (relalg.Predicate, error) {
	n := 0
	preds, err := filterPredicates(conds, 0, &n)
	if err != nil {
		return nil, err
	}
	if maxConds > 0 && n > maxConds {
		return nil, fmt.Errorf("%w: too many filter conditions", ErrInvalidInput)
	}
	switch len(preds) {
	case 0:
		return nil, nil
	case 1:
		return preds[0], nil
	}
	return relalg.And{Preds: preds}, nil
}

// maxFilterDepth bounds how deeply filter groups nest.
const maxFilterDepth = 4

func filterPredicates(conds []condJSON, depth int, n *int) ([]relalg.Predicate, error) {
	preds := make([]relalg.Predicate, 0, len(conds))
	for _, c := range conds {
		p, err := filterPredicate(c, depth, n)
		if err != nil {
			return nil, err
		}
		preds = append(preds, p)
	}
	return preds, nil
}

// filterPredicate converts one condition, or one any/all/not group, of a
// filter at depth (0 at the top).
func filterPredicate(c condJSON, depth int, n *int) (relalg.Predicate, error) {
	forms := 0
	for _, set := range []bool{c.Field != "" || c.Op != "" || c.Value != nil, c.Any != nil, c.All != nil,
		c.Not != nil, c.Exists != ""} {
		if set {
			forms++
		}
	}
	if forms != 1 || c.Filter != nil && c.Exists == "" {
		return nil, fmt.Errorf("%w: a filter entry is one condition (field, op, value) or one of any, all, not, "+
			"exists (with its filter)", ErrInvalidInput)
	}
	if (c.Any != nil || c.All != nil || c.Not != nil || c.Exists != "") && depth >= maxFilterDepth {
		return nil, fmt.Errorf("%w: filter groups nest at most %d deep", ErrInvalidInput, maxFilterDepth)
	}
	switch {
	case c.Exists != "":
		// Resolved on the relationship's target (see resolveExists).
		preds, err := filterPredicates(c.Filter, depth+1, n)
		if err != nil {
			return nil, err
		}
		return relalg.Exists{Relation: relalg.RelationRef{Name: c.Exists}, Predicate: conjunction(preds)}, nil
	case c.Not != nil:
		p, err := filterPredicate(*c.Not, depth+1, n)
		if err != nil {
			return nil, err
		}
		return relalg.Not{P: p}, nil
	case c.Any != nil || c.All != nil:
		return filterGroup(c, depth, n)
	}
	*n++
	op := relalg.Op(c.Op)
	if !op.Valid() {
		return nil, fmt.Errorf("%w: operator %q", ErrInvalidInput, c.Op)
	}
	value, err := normalizeJSONValue(c.Value)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	// A comparison with null holds for no row: it is a mistake for is_null.
	if value == nil && op != relalg.OpIsNull && op != relalg.OpIsNotNull {
		return nil, fmt.Errorf("%w: %s compares with null, which holds for no row; use is_null or is_not_null",
			ErrInvalidInput, op)
	}
	return relalg.Condition{Field: c.Field, Op: op, Value: value}, nil
}

// filterFields extracts the field names referenced by a filter, through its
// groups.
func filterFields(conds []condJSON) []string {
	out := make([]string, 0, len(conds))
	var walk func(c condJSON)
	walk = func(c condJSON) {
		switch {
		case c.Exists != "": // the target's fields (see resolveExists)
		case c.Not != nil:
			walk(*c.Not)
		case c.Any != nil || c.All != nil:
			for _, g := range append(slices.Clone(c.Any), c.All...) {
				walk(g)
			}
		default:
			out = append(out, c.Field)
		}
	}
	for _, c := range conds {
		walk(c)
	}
	return out
}

// filterGroup converts an any (OR) or all (AND) group at depth.
func filterGroup(c condJSON, depth int, n *int) (relalg.Predicate, error) {
	group := c.All
	if c.Any != nil {
		group = c.Any
	}
	if len(group) == 0 {
		return nil, fmt.Errorf("%w: an empty filter group", ErrInvalidInput)
	}
	preds, err := filterPredicates(group, depth+1, n)
	if err != nil {
		return nil, err
	}
	if len(preds) == 1 {
		return preds[0], nil
	}
	if c.Any != nil {
		return relalg.Or{Preds: preds}, nil
	}
	return relalg.And{Preds: preds}, nil
}
