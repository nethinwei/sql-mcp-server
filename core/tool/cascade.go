package tool

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
)

// cascadeEffect is what one foreign key does to its referencing relation
// when rows of the referenced one change: deletes them, or updates columns.
type cascadeEffect struct {
	cascade entity.Cascade
	action  entity.Action
	columns []string // the referencing columns an update writes
}

// cascadeEffects lists the referential writes a delete of e's rows, or an
// update of its columns updated, triggers directly.
func cascadeEffects(e entity.Entity, action entity.Action, updated []string) []cascadeEffect {
	var out []cascadeEffect
	for _, c := range e.Cascades {
		rule := c.OnDelete
		if action == entity.ActionUpdate {
			rule = c.OnUpdate
			if !slices.ContainsFunc(c.Columns, func(col string) bool { return slices.Contains(updated, col) }) {
				continue
			}
		}
		switch {
		case !entity.ReferentialWrite(rule):
		case action == entity.ActionDelete && rule == entity.FKCascade:
			out = append(out, cascadeEffect{cascade: c, action: entity.ActionDelete})
		default:
			out = append(out, cascadeEffect{cascade: c, action: entity.ActionUpdate, columns: c.ForeignColumns})
		}
	}
	return out
}

// checkCascades enforces that the writes foreign keys cascade from a delete
// (or an update of the referenced columns updated) are covered (I-6): the
// caller needs, on every entity reached through any number of cascading
// foreign keys, the matching permission (an update on the columns it
// rewrites) without a row scope, since the database rewrites all of the
// referencing rows. A cascade into a relation no entity exposes cannot be
// covered. AllowCascade skips the check from that entity on.
func checkCascades(ctx context.Context, tc Context, e entity.Entity, action entity.Action, updated []string) error {
	return walkCascades(ctx, tc, e, action, columnNames(e, updated), map[string]bool{})
}

func walkCascades(
	ctx context.Context, tc Context, e entity.Entity, action entity.Action, updated []string, seen map[string]bool,
) error {
	key := e.Name + "\x00" + string(action) + "\x00" + strings.Join(updated, "\x00")
	if e.AllowCascade || seen[key] {
		return nil
	}
	seen[key] = true
	for _, effect := range cascadeEffects(e, action, updated) {
		c := effect.cascade
		if c.Entity == "" {
			return fmt.Errorf("%w: the write cascades to %s, which no entity exposes", ErrUnauthorized, c.Table)
		}
		child, ok := tc.Registry.Resolve(c.Entity)
		if !ok {
			return fmt.Errorf("%w: the write cascades to %q, which is not registered", ErrUnauthorized, c.Entity)
		}
		dec, err := authorize(ctx, tc, rbac.Request{
			Role: tc.Role, Subject: tc.Subject, Entity: c.Entity, Action: effect.action, WriteFields: effect.columns,
		})
		if err != nil {
			return err
		}
		if !dec.Allowed || dec.RowFilter != nil {
			return fmt.Errorf("%w: the write cascades to %q, which needs %s permission there without a row scope",
				ErrUnauthorized, c.Entity, effect.action)
		}
		if err := walkCascades(ctx, tc, child.Entity, effect.action, effect.columns, seen); err != nil {
			return err
		}
	}
	return nil
}

// columnNames maps the fields a request names (column names or aliases) to
// column names.
func columnNames(e entity.Entity, fields []string) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if a, ok := e.AttributeByName(f); ok {
			f = a.Name
		}
		out = append(out, f)
	}
	return out
}
