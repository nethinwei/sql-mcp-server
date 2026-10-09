package configplan

import (
	"slices"

	"github.com/nethinwei/sql-mcp-server/core/config"
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
				for _, f := range filterKeys(g.Rows) {
					use(id, f, "rows of "+p.String())
				}
			}
		}
	}
	return ev.uses
}

func (ev *evaluation) entityUses(e config.EntityConfig, use func(id, field, where string)) {
	id := e.ID()
	for _, f := range e.PrimaryKey {
		use(id, f, "primaryKey")
	}
	for _, k := range e.UniqueKeys {
		for _, f := range k {
			use(id, f, "uniqueKeys")
		}
	}
	for _, f := range filterKeys(e.TenantPolicy) {
		use(id, f, "access")
	}
	for _, policy := range e.RowPolicies {
		for _, f := range filterKeys(policy) {
			use(id, f, "access")
		}
	}
	for _, acl := range e.FieldACL {
		for _, f := range slices.Concat(acl.Read, acl.Write) {
			use(id, f, "access")
		}
	}
	for _, r := range e.Relationships {
		where := "relationship " + id + "." + r.Name
		target, ok := ev.resolve(r.Target)
		for local, remote := range r.JoinOn {
			use(id, local, where)
			if ok {
				use(target, remote, where)
			}
		}
	}
}

// fieldReferences lists what references a field of the entity id besides
// grants' field lists, which follow a removal.
func (ev *evaluation) fieldReferences(id, field string) []string {
	out := slices.Clone(ev.fieldUses()[id][field])
	slices.Sort(out)
	return out
}

// filterKeys lists the keys a filter (decoded YAML or JSON) names anywhere:
// the fields it reads.
func filterKeys(v any) []string {
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, inner := range x {
				out = append(out, k)
				walk(inner)
			}
		case []any:
			for _, inner := range x {
				walk(inner)
			}
		}
	}
	walk(v)
	return out
}

// dropGrantField removes a field from the field lists of the grants on id.
func (ev *evaluation) dropGrantField(id, field string) {
	for _, p := range ev.principals() {
		grants := ev.grantsOf(p)
		var out []config.GrantConfig
		for i, g := range grants {
			if g.Fields == nil || !slices.Contains(g.Fields.Read, field) && !slices.Contains(g.Fields.Write, field) {
				continue
			}
			if on, ok := ev.resolve(g.Entity); !ok || on != id {
				continue
			}
			if out == nil {
				out = slices.Clone(grants)
			}
			without := func(fs []string) []string {
				return slices.DeleteFunc(slices.Clone(fs), func(f string) bool { return f == field })
			}
			out[i].Fields = &config.FieldACLConfig{Read: without(g.Fields.Read), Write: without(g.Fields.Write)}
		}
		if out != nil {
			ev.setGrants(p, out)
		}
	}
}

// qualifyReferences rewrites the references of the base configuration that
// would name several entities once the plan added its own to the ID of the
// entity they named, as the console does when tables are imported, and
// reports each rewrite.
func (ev *evaluation) qualifyReferences() {
	if len(ev.added) == 0 {
		return
	}
	before, after := config.NewEntityRefs(ev.in.Base.Entities), ev.resolver()
	fix := func(owner, ref string) string {
		e, err := before.Resolve(ref)
		if err != nil {
			return ref
		}
		if _, err := after.Resolve(ref); err == nil {
			return ref
		}
		ev.except(Exception{Kind: ExceptionQualified, Entity: owner, Detail: ref + " → " + e.ID()})
		return e.ID()
	}
	for _, p := range ev.principals() {
		grants := slices.Clone(ev.grantsOf(p))
		changed := false
		for i, g := range grants {
			if ref := fix(p.String(), g.Entity); ref != g.Entity {
				grants[i].Entity, changed = ref, true
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

func (ev *evaluation) qualifyEntity(e *config.EntityConfig, fix func(owner, ref string) string) {
	id := e.ID()
	rels, affects := slices.Clone(e.Relationships), slices.Clone(e.Affects)
	changed := false
	for i, r := range rels {
		if ref := fix(id, r.Target); ref != r.Target {
			rels[i].Target, changed = ref, true
		}
	}
	for i, a := range affects {
		if ref := fix(id, a); ref != a {
			affects[i], changed = ref, true
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
		Config: ev.cfg, Added: ev.added, Roles: map[string]config.RoleDefinition{}, Users: map[string]config.UserConfig{},
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
