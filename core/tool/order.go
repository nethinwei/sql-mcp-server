package tool

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

// maxOrderTerms bounds a request's orderBy.
const maxOrderTerms = 8

// readOrder is how a read orders its rows when it names an orderBy: the
// terms (the fields asked for, then the cursor key's columns not among
// them, so the order is total), and whether it pages by them (a cursor in,
// nextCursor out): the key is among them.
type readOrder struct {
	terms    []relalg.OrderTerm
	pageable bool
	// keyFields are the key's columns added to the terms.
	keyFields []string
}

// fields are the columns the terms name.
func (o readOrder) fields() []string {
	out := make([]string, len(o.terms))
	for i, t := range o.terms {
		out[i] = t.Field
	}
	return out
}

// planReadOrder checks and resolves in.OrderBy on res: each field visible
// and unmasked (an order would reveal a masked value), named once, asc or
// desc. A distinct read orders by fields it selects only, and ends in no key.
func planReadOrder(res entity.Resolved, in readInput) (readOrder, error) {
	if len(in.OrderBy) == 0 {
		return readOrder{}, nil
	}
	if len(in.OrderBy) > maxOrderTerms {
		return readOrder{}, fmt.Errorf("%w: orderBy names at most %d fields", ErrInvalidInput, maxOrderTerms)
	}
	var o readOrder
	for _, t := range in.OrderBy {
		dir := strings.ToLower(t.Dir)
		if dir == "" {
			dir = "asc"
		}
		if dir != "asc" && dir != "desc" {
			return readOrder{}, fmt.Errorf("%w: orderBy dir %q: want asc or desc", ErrInvalidInput, t.Dir)
		}
		if err := validateFields(res, t.Field); err != nil {
			return readOrder{}, err
		}
		if err := validateUnmaskedFields(res, t.Field); err != nil {
			return readOrder{}, err
		}
		a, _ := res.Entity.AttributeByName(t.Field)
		if slices.Contains(o.fields(), a.Name) {
			return readOrder{}, fmt.Errorf("%w: orderBy names %q twice", ErrInvalidInput, t.Field)
		}
		if in.Distinct && !slices.ContainsFunc(in.Fields, func(f string) bool { return f == a.Name || f == a.Alias }) {
			return readOrder{}, fmt.Errorf("%w: a distinct read orders by fields it selects; %q is not one",
				ErrInvalidInput, t.Field)
		}
		// A distinct read orders by its select list alone (PostgreSQL), so
		// NULLs keep the database's place there.
		nullable := a.Domain.Nullable && !in.Distinct
		o.terms = append(o.terms, relalg.OrderTerm{Field: a.Name, Dir: dir, Nullable: nullable})
	}
	if key := res.Entity.CursorKey(); !in.Distinct && key != nil && !hasMaskedField(res, key) {
		for _, c := range key {
			if !slices.Contains(o.fields(), c) {
				o.terms = append(o.terms, relalg.OrderTerm{Field: c, Dir: "asc"})
				o.keyFields = append(o.keyFields, c)
			}
		}
		o.pageable = true
	}
	return o, nil
}

func hasMaskedField(res entity.Resolved, fields []string) bool {
	return validateUnmaskedFields(res, fields...) != nil
}

// withoutKey is o ordering by the fields asked for alone: the caller cannot
// read the key, so the order is not total and does not page.
func (o readOrder) withoutKey() readOrder {
	o.terms = slices.DeleteFunc(slices.Clone(o.terms), func(t relalg.OrderTerm) bool {
		return slices.Contains(o.keyFields, t.Field)
	})
	o.pageable, o.keyFields = false, nil
	return o
}

// readsKey reports whether the caller may read the key columns o adds along
// with the fields req reads: asked of the authorizer alone, as a probe, so
// a no is not reported as a denial.
func readsKey(ctx context.Context, tc Context, req rbac.Request, o readOrder) bool {
	if len(o.keyFields) == 0 || tc.Authorizer == nil {
		return true
	}
	req.ReadFields = append(slices.Clone(req.ReadFields), o.keyFields...)
	dec, err := tc.Authorizer.Authorize(ctx, req)
	return err == nil && dec.Allowed
}

// orderCursor resumes a read ordered by o after cursor: the values of every
// term's field in the last row read. It holds when one of the fields comes
// after its value in the order and the ones before it are equal; NULLs come
// last whatever the direction, so after a value come the greater (or
// lesser) ones and NULL, and after NULL nothing.
func orderCursor(o readOrder, cursor map[string]any) (relalg.Predicate, error) {
	if !o.pageable {
		return nil, fmt.Errorf("%w: this order does not page by cursor (no key the caller reads): use offset",
			ErrInvalidInput)
	}
	wrong := fmt.Errorf("%w: the cursor names the ordered fields (%s): pass nextCursor as it is",
		ErrInvalidInput, strings.Join(o.fields(), ", "))
	if len(cursor) != len(o.terms) {
		return nil, wrong
	}
	var ors []relalg.Predicate
	for i, t := range o.terms {
		v, ok := cursor[t.Field]
		if !ok || v == nil && !t.Nullable {
			return nil, wrong
		}
		after := afterValue(t, v)
		if after == nil {
			continue
		}
		ands := make([]relalg.Predicate, 0, i+1)
		for _, before := range o.terms[:i] {
			ands = append(ands, equalsValue(before.Field, cursor[before.Field]))
		}
		ors = append(ors, conjunction(append(ands, after)))
	}
	switch len(ors) {
	case 0: // the key is never NULL: not a cursor of this order
		return nil, wrong
	case 1:
		return ors[0], nil
	}
	return relalg.Or{Preds: ors}, nil
}

// afterValue holds for the values of t's field ordered after v; nil when
// none is (v is NULL, which comes last).
func afterValue(t relalg.OrderTerm, v any) relalg.Predicate {
	if v == nil {
		return nil
	}
	op := relalg.OpGt
	if t.Dir == "desc" {
		op = relalg.OpLt
	}
	past := relalg.Condition{Field: t.Field, Op: op, Value: v}
	if !t.Nullable {
		return past
	}
	return relalg.Or{Preds: []relalg.Predicate{past, relalg.Condition{Field: t.Field, Op: relalg.OpIsNull}}}
}

// equalsValue holds for field equal to v, NULL included.
func equalsValue(field string, v any) relalg.Predicate {
	if v == nil {
		return relalg.Condition{Field: field, Op: relalg.OpIsNull}
	}
	return relalg.Condition{Field: field, Op: relalg.OpEq, Value: v}
}

func conjunction(preds []relalg.Predicate) relalg.Predicate {
	switch len(preds) {
	case 0:
		return nil
	case 1:
		return preds[0]
	}
	return relalg.And{Preds: preds}
}

// nextCursor is where the read after rows resumes: the ordered fields of
// the last row, when the read filled its limit and its order pages.
func nextCursor(o readOrder, res entity.Resolved, in readInput, rows []map[string]any) map[string]any {
	if !o.pageable || in.Limit <= 0 || int64(len(rows)) < in.Limit {
		return nil
	}
	last := rows[len(rows)-1]
	out := make(map[string]any, len(o.terms))
	for _, t := range o.terms {
		v, ok := last[t.Field]
		if a, found := res.Entity.AttributeByName(t.Field); !ok && found && a.Alias != "" {
			v, ok = last[a.Alias]
		}
		if !ok {
			return nil
		}
		out[t.Field] = v
	}
	return out
}

// trimOrderFields drops from rows the ordered fields the read added to its
// projection to build nextCursor.
func trimOrderFields(o readOrder, res entity.Resolved, in readInput, rows []map[string]any) {
	if len(in.Fields) == 0 {
		return
	}
	for _, f := range o.fields() {
		a, _ := res.Entity.AttributeByName(f)
		if slices.Contains(in.Fields, a.Name) || a.Alias != "" && slices.Contains(in.Fields, a.Alias) {
			continue
		}
		for _, row := range rows {
			delete(row, f)
		}
	}
}
