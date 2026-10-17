package tool

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

// A read or an aggregate names its entity's relationships two ways:
//
//   - relationship.field, where the relationship names one row (one,
//     one-to-one, belongs-to), in a filter, an order or a group-by: the
//     target is joined (a left join on the target's key; a row whose target
//     the caller cannot see, or which has none, sees NULLs);
//   - {"exists": relationship, "filter": [...]} in a filter, for any
//     relationship: some target row matches.
//
// The caller must read the fields it names of the target, and the target's
// row policy applies to the rows it joins or finds; the relationship's own
// columns are read of the entity. Masked fields are refused, as on the
// entity; the target stays on the entity's datasource.

// related is what a request names of its entity's relationships: the joins
// it needs, the entity's columns they read, and how the fields it named
// across them are written in the IR (relationship.column).
type related struct {
	joins  []relalg.Join
	locals []string
	rename map[string]string
}

// pathOf splits field into a relationship of e and the target's field.
func pathOf(e entity.Entity, field string) (entity.Relationship, string, bool) {
	name, rest, ok := strings.Cut(field, ".")
	if !ok {
		return entity.Relationship{}, "", false
	}
	r, found := relationshipByName(e, name)
	return r, rest, found
}

// splitPaths are fields without the paths across e's relationships, and the
// paths.
func splitPaths(e entity.Entity, fields []string) (own, paths []string) {
	for _, f := range fields {
		if _, _, ok := pathOf(e, f); ok {
			paths = append(paths, f)
		} else {
			own = append(own, f)
		}
	}
	return own, paths
}

// relatedTarget is the target of r, authorized for reading fields (and the
// relationship's columns), on e's datasource: its entity and the row filter
// that applies to it.
func relatedTarget(ctx context.Context, tc Context, e entity.Entity, r entity.Relationship, fields []string) (
	entity.Resolved, grant, error,
) {
	target, err := resolveDMLEntity(tc, r.Target)
	if err != nil {
		return entity.Resolved{}, grant{}, err
	}
	if target.Entity.DataSource != e.DataSource {
		return entity.Resolved{}, grant{}, fmt.Errorf("%w: cross-datasource relationship %q", ErrInvalidInput, r.Name)
	}
	if err := validateFields(target, fields...); err != nil {
		return entity.Resolved{}, grant{}, err
	}
	if err := validateUnmaskedFields(target, fields...); err != nil {
		return entity.Resolved{}, grant{}, err
	}
	join := relationJoin(r)
	g, err := authorizeRequest(ctx, tc, rbac.Request{
		Role: tc.Role, Subject: tc.Subject, Entity: target.Entity.Name, Action: entity.ActionRead,
		ReadFields: append(slices.Clone(fields), join.target...),
	})
	return target, g, err
}

// planJoins joins the targets of the relationships paths name: each a
// relationship naming one row by the target's key.
func planJoins(ctx context.Context, tc Context, res entity.Resolved, paths []string) (related, error) {
	out := related{rename: map[string]string{}}
	byRelation := map[string][]string{}
	for _, p := range paths {
		r, field, _ := pathOf(res.Entity, p)
		byRelation[r.Name] = append(byRelation[r.Name], field)
	}
	for _, name := range slices.Sorted(maps.Keys(byRelation)) {
		r, _ := relationshipByName(res.Entity, name)
		if !r.Single() {
			return related{}, fmt.Errorf("%w: relationship %q names many rows: filter by it with "+
				`{"exists": %q, "filter": [...]}`, ErrInvalidInput, name, name)
		}
		target, g, err := relatedTarget(ctx, tc, res.Entity, r, byRelation[name])
		if err != nil {
			return related{}, err
		}
		join := relationJoin(r)
		if !slices.ContainsFunc(target.Entity.IdentityKeys(false, false), func(k []string) bool {
			return len(k) == len(join.target) && !slices.ContainsFunc(k, func(c string) bool {
				return !slices.Contains(join.target, c)
			})
		}) {
			return related{}, fmt.Errorf("%w: relationship %q does not join on a key of %q, so it may name "+
				"several rows: filter by it with exists", ErrInvalidInput, name, target.Entity.Name)
		}
		for _, f := range byRelation[name] {
			a, _ := target.Entity.AttributeByName(f)
			out.rename[name+"."+f] = name + "." + a.Name
		}
		out.joins = append(out.joins, relalg.Join{Right: target.Entity.Ref(), Alias: name,
			On: joinPairs(join), RightPredicate: g.RowFilter})
		out.locals = append(out.locals, join.local...)
	}
	return out, nil
}

func joinPairs(join joinColumns) []relalg.JoinPair {
	out := make([]relalg.JoinPair, len(join.local))
	for i := range join.local {
		out[i] = relalg.JoinPair{Left: join.local[i], Right: join.target[i]}
	}
	return out
}

// wrap joins expr (the entity's scan) to the targets.
func (r related) wrap(expr relalg.Expr) relalg.Expr {
	for _, j := range r.joins {
		j.Input = expr
		expr = j
	}
	return expr
}

// field is how the IR names field: a path by its target's column.
func (r related) field(f string) string {
	if to, ok := r.rename[f]; ok {
		return to
	}
	return f
}

// renamed is p with its paths named as the IR names them.
func (r related) renamed(p relalg.Predicate) relalg.Predicate {
	switch pp := p.(type) {
	case relalg.Condition:
		pp.Field = r.field(pp.Field)
		return pp
	case relalg.And:
		return relalg.And{Preds: r.renamedAll(pp.Preds)}
	case relalg.Or:
		return relalg.Or{Preds: r.renamedAll(pp.Preds)}
	case relalg.Not:
		return relalg.Not{P: r.renamed(pp.P)}
	}
	return p
}

func (r related) renamedAll(preds []relalg.Predicate) []relalg.Predicate {
	out := make([]relalg.Predicate, len(preds))
	for i, p := range preds {
		out[i] = r.renamed(p)
	}
	return out
}

// resolveExists resolves the exists groups of p, a filter on res: each to
// the relationship's target, its filter checked on it and its row policy
// added. It returns the entity's columns the relationships read.
func resolveExists(ctx context.Context, tc Context, res entity.Resolved, p relalg.Predicate) (
	relalg.Predicate, []string, error,
) {
	var locals []string
	var walk func(relalg.Predicate) (relalg.Predicate, error)
	walk = func(p relalg.Predicate) (relalg.Predicate, error) {
		switch pp := p.(type) {
		case relalg.Exists:
			r, ok := relationshipByName(res.Entity, pp.Relation.Name)
			if !ok || len(pp.On) > 0 {
				return nil, fmt.Errorf("%w: exists names %q, not a relationship of %q", ErrInvalidInput,
					pp.Relation.Name, res.Entity.Name)
			}
			fields := conditionFields(pp.Predicate)
			if relalg.HasExists(pp.Predicate) {
				return nil, fmt.Errorf("%w: exists does not nest", ErrInvalidInput)
			}
			target, g, err := relatedTarget(ctx, tc, res.Entity, r, fields)
			if err != nil {
				return nil, err
			}
			join := relationJoin(r)
			locals = append(locals, join.local...)
			return relalg.Exists{Relation: target.Entity.Ref(), On: joinPairs(join),
				Predicate: andPreds(pp.Predicate, g.RowFilter)}, nil
		case relalg.And:
			preds, err := walkAll(pp.Preds, walk)
			return relalg.And{Preds: preds}, err
		case relalg.Or:
			preds, err := walkAll(pp.Preds, walk)
			return relalg.Or{Preds: preds}, err
		case relalg.Not:
			q, err := walk(pp.P)
			return relalg.Not{P: q}, err
		}
		return p, nil
	}
	out, err := walk(p)
	return out, locals, err
}

func walkAll(preds []relalg.Predicate, walk func(relalg.Predicate) (relalg.Predicate, error)) (
	[]relalg.Predicate, error,
) {
	out := make([]relalg.Predicate, len(preds))
	for i, p := range preds {
		q, err := walk(p)
		if err != nil {
			return nil, err
		}
		out[i] = q
	}
	return out, nil
}

// conditionFields are the fields p's conditions name, not crossing into an
// EXISTS.
func conditionFields(p relalg.Predicate) []string {
	switch pp := p.(type) {
	case relalg.Condition:
		return []string{pp.Field}
	case relalg.And:
		return fieldsOfAll(pp.Preds)
	case relalg.Or:
		return fieldsOfAll(pp.Preds)
	case relalg.Not:
		return conditionFields(pp.P)
	}
	return nil
}

func fieldsOfAll(preds []relalg.Predicate) []string {
	var out []string
	for _, p := range preds {
		out = append(out, conditionFields(p)...)
	}
	return out
}
