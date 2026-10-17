package tool

import (
	"fmt"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

// A field is named by its column or its alias (see entity.Attribute); SQL
// names the column. What a request names reaches the IR as columns, and a
// projected field comes back as it was asked for.

// columnOf is the column field names: the attribute's whose name or alias
// it is, else field itself (a path across a relationship, or an unknown
// field the caller's checks refuse).
func columnOf(e entity.Entity, field string) string {
	if a, ok := e.AttributeByName(field); ok {
		return a.Name
	}
	return field
}

// columnsPredicate is p with its conditions naming columns. An exists group
// names its target's fields, named there (see resolveExists).
func columnsPredicate(e entity.Entity, p relalg.Predicate) relalg.Predicate {
	switch pp := p.(type) {
	case relalg.Condition:
		pp.Field = columnOf(e, pp.Field)
		return pp
	case relalg.And:
		return relalg.And{Preds: columnsPredicates(e, pp.Preds)}
	case relalg.Or:
		return relalg.Or{Preds: columnsPredicates(e, pp.Preds)}
	case relalg.Not:
		return relalg.Not{P: columnsPredicate(e, pp.P)}
	}
	return p
}

func columnsPredicates(e entity.Entity, preds []relalg.Predicate) []relalg.Predicate {
	out := make([]relalg.Predicate, len(preds))
	for i, p := range preds {
		out[i] = columnsPredicate(e, p)
	}
	return out
}

// projectItems project fields from their columns, each named as asked.
func projectItems(e entity.Entity, fields []string) []relalg.ProjectItem {
	items := make([]relalg.ProjectItem, len(fields))
	for i, f := range fields {
		items[i] = relalg.ProjectItem{Field: columnOf(e, f)}
		if items[i].Field != f {
			items[i].Alias = f
		}
	}
	return items
}

// distinctColumns refuses fields naming one column twice (by its name and
// by its alias).
func distinctColumns(e entity.Entity, fields []string) error {
	seen := map[string]bool{}
	for _, c := range columnsOf(e, fields) {
		if seen[c] {
			return fmt.Errorf("%w: field %q is named twice (by its name and its alias)", ErrInvalidInput, c)
		}
		seen[c] = true
	}
	return nil
}

// columnsOf are the columns fields name.
func columnsOf(e entity.Entity, fields []string) []string {
	out := make([]string, len(fields))
	for i, f := range fields {
		out[i] = columnOf(e, f)
	}
	return out
}
