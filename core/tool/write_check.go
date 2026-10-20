package tool

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
	"github.com/nethinwei/sql-mcp-server/core/relalg/interp"
)

// A write keeps its rows inside the caller's row scope (the tenant policy
// AND the covering grants' rows), as PostgreSQL's WITH CHECK does: a create
// must insert a row the caller could read, and an update must not move a row
// out of reach (to another tenant, say). See docs/design/authorization-model.md.

// RowPolicyViolationError is a write whose new row would fall outside the
// caller's row scope. Fields are the fields the caller can name whose values
// decide it (none when only hidden columns do).
type RowPolicyViolationError struct {
	Fields []string
	cause  error
}

func (e *RowPolicyViolationError) Error() string {
	msg := "tool: the written row would be outside the caller's row scope"
	if len(e.Fields) > 0 {
		msg += " (fields: " + strings.Join(e.Fields, ", ") + ")"
	}
	return msg
}

func (e *RowPolicyViolationError) Unwrap() error { return e.cause }

// insertColumns are the columns and values a create inserts: the caller's
// values by column, plus the value the row scope requires of each column it
// pins by equality (a tenant column, typically hidden from the caller) that
// the caller left out. The row must satisfy the scope.
func insertColumns(e entity.Entity, scope relalg.Predicate, values map[string]any) ([]string, relalg.Tuple, error) {
	row := make(map[string]any, len(values))
	for field, v := range values {
		row[columnOf(e, field)] = v
	}
	for column, v := range pinned(scope) {
		if _, ok := row[column]; !ok {
			row[column] = v
		}
	}
	// A column the scope tests but the row leaves out is decided by the
	// database (a default), which the check cannot see: Depends refuses it.
	if _, verdict, err := interp.Check(scope, exactValues(e, row)); verdict != interp.Holds {
		return nil, nil, &RowPolicyViolationError{Fields: scopeFields(e, scope), cause: err}
	}
	columns := slices.Sorted(maps.Keys(row))
	tuple := make(relalg.Tuple, len(columns))
	for i, c := range columns {
		tuple[i] = row[c]
	}
	return columns, tuple, nil
}

// updateCheck is the predicate an update adds to keep each row in scope:
// the scope with the assigned columns decided by their new values. An
// assignment that takes every row out of scope is refused.
func updateCheck(e entity.Entity, scope relalg.Predicate, set map[string]any) (relalg.Predicate, error) {
	assigned := make(map[string]any, len(set))
	for field, v := range set {
		assigned[columnOf(e, field)] = v
	}
	residual, verdict, err := interp.Check(scope, exactValues(e, assigned))
	switch verdict {
	case interp.Holds:
		return nil, nil
	case interp.Depends:
		return residual, nil
	default:
		return nil, &RowPolicyViolationError{Fields: scopeFields(e, scope), cause: err}
	}
}

// exactValues are row's values, by column, as the check compares them: a
// decimal column's text as the number it holds.
func exactValues(e entity.Entity, row map[string]any) map[string]any {
	out := make(map[string]any, len(row))
	for column, v := range row {
		if i := slices.IndexFunc(e.Attributes, func(a entity.Attribute) bool { return a.Name == column }); i >= 0 {
			v = exactValue(e.Attributes[i].Domain.Type, v)
		}
		out[column] = v
	}
	return out
}

// pinned are the columns scope requires to equal one value: its equality
// conditions on a scalar, alone or ANDed at any depth (the tenant policy
// and a grant's rows are each ANDs of their own, ANDed together).
func pinned(scope relalg.Predicate) map[string]any {
	out := map[string]any{}
	var walk func(relalg.Predicate)
	walk = func(p relalg.Predicate) {
		switch p := p.(type) {
		case relalg.And:
			for _, q := range p.Preds {
				walk(q)
			}
		case relalg.Condition:
			if p.Op != relalg.OpEq || p.Value == nil {
				return
			}
			if _, list := p.Value.([]any); !list {
				out[p.Field] = p.Value
			}
		}
	}
	walk(scope)
	return out
}

// scopeFields names the visible fields scope tests, as the caller names them.
func scopeFields(e entity.Entity, scope relalg.Predicate) []string {
	var out []string
	for _, column := range relalg.Fields(scope) {
		i := slices.IndexFunc(e.Attributes, func(a entity.Attribute) bool { return a.Name == column })
		if i < 0 || e.Attributes[i].Excluded {
			continue
		}
		a := e.Attributes[i]
		name := a.Name
		if a.Alias != "" {
			name = a.Alias
		}
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// refuseKeyUpdate refuses assigning the columns that identify a row (the
// primary key, else the first identity key) unless the entity allows it.
func refuseKeyUpdate(e entity.Entity, fields []string) error {
	if e.AllowKeyUpdate {
		return nil
	}
	key := e.CursorKey()
	for _, f := range fields {
		if slices.Contains(key, columnOf(e, f)) {
			return fmt.Errorf("%w: field %q identifies the row and cannot be updated "+
				"(delete the row and create it again)", ErrInvalidInput, f)
		}
	}
	return nil
}
