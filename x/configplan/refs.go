package configplan

import (
	"slices"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/x/configrefs"
)

// resolver resolves references among the entities, the plan's included: it
// is built once the plan's entities are added, as their IDs do not change
// after.
func (ev *evaluation) resolver() *config.EntityRefs {
	if ev.refs == nil {
		refs := config.NewEntityRefs(ev.cfg.Entities)
		ev.refs = &refs
	}
	return ev.refs
}

func (ev *evaluation) resolve(ref string) (string, bool) {
	e, err := ev.resolver().Resolve(ref)
	return e.ID(), err == nil
}

// fieldUses indexes, in one pass, what names each field of each entity
// besides grants' field lists (which follow a removal): keys, access
// policies, relationships on either side and the row filters of grants.
// Removing fields changes none of these, so it is built once.
func (ev *evaluation) fieldUses() map[string]map[string][]string {
	if ev.uses != nil {
		return ev.uses
	}
	ev.uses = map[string]map[string][]string{}
	use := func(id, field, where string) {
		if ev.uses[id] == nil {
			ev.uses[id] = map[string][]string{}
		}
		if !slices.Contains(ev.uses[id][field], where) {
			ev.uses[id][field] = append(ev.uses[id][field], where)
		}
	}
	for _, e := range ev.cfg.Entities {
		ev.entityUses(e, use)
	}
	for _, p := range ev.principals() {
		for _, g := range ev.grantsOf(p) {
			if id, ok := ev.resolve(g.Entity); ok {
				for _, f := range configrefs.FilterFields(g.Rows) {
					use(id, f, "rows of "+p.String())
				}
			}
		}
	}
	return ev.uses
}

func (ev *evaluation) entityUses(e config.EntityConfig, use func(id, field, where string)) {
	configrefs.FieldUses(e, ev.resolve, func(u configrefs.FieldUse) {
		switch u.Kind {
		case configrefs.UsePrimaryKey:
			use(u.Entity, u.Field, "primaryKey")
		case configrefs.UseUniqueKey:
			use(u.Entity, u.Field, "uniqueKeys")
		case configrefs.UseRelationship:
			use(u.Entity, u.Field, "relationship "+e.ID()+"."+u.Relationship)
		case configrefs.UseTenantPolicy, configrefs.UseFieldACL, configrefs.UseRowPolicy:
			use(u.Entity, u.Field, "access")
		default: // a use this does not know: still one, named as it is
			use(u.Entity, u.Field, u.Kind)
		}
	})
}

// fieldReferences lists what references a field of the entity id besides
// grants' field lists, which follow a removal.
func (ev *evaluation) fieldReferences(id, field string) []string {
	out := slices.Clone(ev.fieldUses()[id][field])
	slices.Sort(out)
	return out
}

// dropGrantFields removes the fields a sync removed (dropped) from the field
// lists of the grants on their entities, in one pass over the grants: a
// sync may remove thousands.
func (ev *evaluation) dropGrantFields() {
	if len(ev.dropped) == 0 {
		return
	}
	for _, p := range ev.principals() {
		grants := ev.grantsOf(p)
		var out []config.GrantConfig
		for i, g := range grants {
			if g.Fields == nil {
				continue
			}
			on, ok := ev.resolve(g.Entity)
			gone := ev.dropped[on]
			if !ok || !slices.ContainsFunc(slices.Concat(g.Fields.Read, g.Fields.Write), func(f string) bool {
				return slices.Contains(gone, f)
			}) {
				continue
			}
			if out == nil {
				out = slices.Clone(grants)
			}
			without := func(fs []string) []string {
				return slices.DeleteFunc(slices.Clone(fs), func(f string) bool { return slices.Contains(gone, f) })
			}
			out[i].Fields = &config.FieldACLConfig{Read: without(g.Fields.Read), Write: without(g.Fields.Write)}
		}
		if out != nil {
			ev.setGrants(p, out)
		}
	}
}

// qualifyReferences keeps the references of the base configuration naming
// the entities they named once the plan added and removed its own
// (configrefs.Retarget: one the additions made ambiguous, or that now names
// an added entity, is rewritten in its shortest unambiguous form, and one
// naming a removed entity goes), as console edits do, and reports each
// rewrite.
func (ev *evaluation) qualifyReferences() {
	if len(ev.added) == 0 && len(ev.removed) == 0 {
		return
	}
	before, after := configrefs.NewIndex(ev.in.Base.Entities), configrefs.NewIndex(ev.cfg.Entities)
	moved := map[string]string{}
	for _, id := range ev.removed {
		moved[id] = ""
	}
	fix := func(owner, ref string) (string, bool) {
		out, keep := configrefs.Retarget(ref, before, after, moved)
		if keep && out != ref {
			ev.except(Exception{Kind: ExceptionQualified, Entity: owner, Detail: ref + " → " + out})
		}
		return out, keep
	}
	for _, p := range ev.principals() {
		var grants []config.GrantConfig
		changed := false
		for _, g := range ev.grantsOf(p) {
			ref, keep := fix(p.String(), g.Entity)
			changed = changed || !keep || ref != g.Entity
			if keep {
				g.Entity = ref
				grants = append(grants, g)
			}
		}
		if changed {
			ev.setGrants(p, grants)
		}
	}
	for i := range ev.cfg.Entities {
		ev.qualifyEntity(&ev.cfg.Entities[i], fix)
	}
}

func (ev *evaluation) qualifyEntity(e *config.EntityConfig, fix func(owner, ref string) (string, bool)) {
	id := e.ID()
	var rels []config.RelationshipConfig
	var affects []string
	changed := false
	for _, r := range e.Relationships {
		ref, keep := fix(id, r.Target)
		changed = changed || !keep || ref != r.Target
		if keep {
			r.Target = ref
			rels = append(rels, r)
		}
	}
	for _, a := range e.Affects {
		ref, keep := fix(id, a)
		changed = changed || !keep || ref != a
		if keep {
			affects = append(affects, ref)
		}
	}
	if changed {
		e.Relationships, e.Affects = rels, affects
		ev.changed[id] = true
	}
}

// proposal puts the outcome together.
func (ev *evaluation) proposal(rows []Row) Proposal {
	out := Proposal{
		Config: ev.cfg, Added: ev.added, Removed: ev.removed,
		Roles: map[string]config.RoleDefinition{}, Users: map[string]config.UserConfig{},
		Report: ev.report,
	}
	for _, e := range ev.cfg.Entities {
		if ev.changed[e.ID()] {
			out.Entities = append(out.Entities, e)
		}
	}
	for name := range ev.roles {
		out.Roles[name] = ev.cfg.Roles[name]
	}
	for name := range ev.users {
		out.Users[name] = ev.cfg.Users[name]
	}
	for _, key := range ev.order {
		out.Report.Changes = append(out.Report.Changes, *ev.changes[key])
	}
	out.Report.Objects = ev.objects(rows)
	return out
}

func (ev *evaluation) objects(rows []Row) []Objects {
	var out []Objects
	for _, r := range rows {
		if n := len(out); n == 0 || out[n-1].Datasource != r.Datasource || out[n-1].Schema != r.Table.Schema {
			out = append(out, Objects{Datasource: r.Datasource, Schema: r.Table.Schema})
		}
		o := &out[len(out)-1]
		o.Tables++
		switch {
		case r.Entity == nil && ev.imported[r.Ref] != "":
			o.Imported++
		case r.Entity != nil && ev.changed[r.Entity.ID()]:
			o.Changed++
		}
	}
	return out
}
