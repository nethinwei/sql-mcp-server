package tool

import (
	"fmt"
	"slices"

	"github.com/nethinwei/sql-mcp-server/core/entity"
)

// fieldUses are the fields of an entity a call uses, by how it uses them;
// each use has its rules, checked in one place (check):
//   - every field used is a visible one (I15): a hidden field can be
//     neither selected, nor compared, nor written;
//   - a compared field (filtered, a cursor, ordered or grouped by,
//     aggregated, a related read's) is not masked (I16): comparing it would
//     reveal its value; a masked field is only ever returned, masked;
//   - a written field is written once, and is not computed by the database.
//
// A new use of fields joins one of these, and so gets its rules.
type fieldUses struct {
	read, compared, written []string
}

// check applies the rules to the uses on res, in that order: a field that
// is not visible is refused first, whatever its use, so the error never
// tells a hidden field from a masked one.
func (u fieldUses) check(res entity.Resolved) error {
	if err := validateFields(res, slices.Concat(u.read, u.compared, u.written)...); err != nil {
		return err
	}
	if err := validateUnmaskedFields(res, u.compared...); err != nil {
		return err
	}
	if len(u.written) == 0 {
		return nil
	}
	if err := distinctColumns(res.Entity, u.written); err != nil {
		return err
	}
	return validateWritable(res, u.written...)
}

// validateFields rejects any field name that is not a visible attribute of the
// entity (by name or alias). This closes a side channel where a filter, GROUP
// BY, or write could reference an excluded/hidden column — e.g. filtering on a
// masked salary to infer its value. Unknown and excluded fields are treated
// identically so the error never reveals which columns exist.
func validateFields(res entity.Resolved, fields ...string) error {
	if len(fields) == 0 {
		return nil
	}
	allowed := make(map[string]bool, len(res.Attributes)*2)
	for _, a := range res.Attributes {
		allowed[a.Name] = true
		if a.Alias != "" {
			allowed[a.Alias] = true
		}
	}
	for _, f := range fields {
		if !allowed[f] {
			return fmt.Errorf("%w: unknown or inaccessible field %q", ErrInvalidInput, f)
		}
	}
	return nil
}

// validateUnmaskedFields rejects masked fields in usages that can reveal or
// compare their underlying values. It intentionally uses the same error as an
// unknown field so callers cannot probe whether a field is masked.
func validateUnmaskedFields(res entity.Resolved, fields ...string) error {
	for _, f := range fields {
		for _, a := range res.Attributes {
			if (a.Name == f || a.Alias != "" && a.Alias == f) && a.Mask != "" {
				return fmt.Errorf("%w: unknown or inaccessible field %q", ErrInvalidInput, f)
			}
		}
	}
	return nil
}

// validateWritable rejects writes to columns the database computes
// (generated columns, identity ALWAYS) before they reach the database.
func validateWritable(res entity.Resolved, fields ...string) error {
	for _, f := range fields {
		if a, ok := res.Entity.AttributeByName(f); ok && !a.Domain.Writable() {
			return fmt.Errorf("%w: field %q is computed by the database and cannot be written", ErrInvalidInput, f)
		}
	}
	return nil
}
