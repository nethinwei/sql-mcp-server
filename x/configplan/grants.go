package configplan

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/reason"
)

// principal is a role or user a grant rule names.
type principal struct{ kind, name string }

func (p principal) String() string { return p.kind + ":" + p.name }

func (ev *evaluation) grantsOf(p principal) []config.GrantConfig {
	if p.kind == "role" {
		return ev.cfg.Roles[p.name].Grants
	}
	return ev.cfg.Users[p.name].Grants
}

// setGrants replaces a principal's grants, marking it changed.
func (ev *evaluation) setGrants(p principal, grants []config.GrantConfig) {
	if p.kind == "role" {
		def := ev.cfg.Roles[p.name]
		def.Grants = grants
		ev.cfg.Roles[p.name] = def
		ev.roles[p.name] = true
		return
	}
	u := ev.cfg.Users[p.name]
	u.Grants = grants
	ev.cfg.Users[p.name] = u
	ev.users[p.name] = true
}

func (ev *evaluation) principals() []principal {
	var out []principal
	for name := range ev.cfg.Roles {
		out = append(out, principal{"role", name})
	}
	for name := range ev.cfg.Users {
		out = append(out, principal{"user", name})
	}
	slices.SortFunc(out, func(a, b principal) int { return strings.Compare(a.String(), b.String()) })
	return out
}

var writes = []string{"update", "delete"}

// grant applies a grant rule to the entities the plan applies to.
func (ev *evaluation) grant(rule GrantRule, targets []target) {
	kind, name, _ := strings.Cut(rule.Principal, ":")
	p := principal{kind, name}
	if kind == "user" {
		if _, ok := ev.cfg.Users[name]; !ok {
			ev.except(Exception{Kind: ExceptionUnknownUser, Detail: name})
			return
		}
	} else if _, ok := ev.cfg.Roles[name]; !ok {
		ev.cfg.Roles[name] = config.RoleDefinition{}
		ev.roles[name] = true
		ev.summarize(Change{Kind: ChangeCreateRole, Principal: rule.Principal}, "", 0)
	}
	// The principal's grants by the entity they name, resolved once: rules
	// cover thousands of tables.
	grants := slices.Clone(ev.grantsOf(p))
	byEntity := map[string][]int{}
	for i, g := range grants {
		if id, ok := ev.resolve(g.Entity); ok {
			byEntity[id] = append(byEntity[id], i)
		}
	}
	changed := false
	for _, t := range targets {
		g, ok := ev.grantFor(rule, t)
		if !ok {
			continue
		}
		at, merged := mergeGrant(grants, byEntity[t.id], g)
		if at < 0 {
			byEntity[t.id] = append(byEntity[t.id], len(grants))
			grants = append(grants, g)
		}
		if at < 0 || merged {
			changed = true
			ev.summarize(Change{Kind: ChangeGrant, Principal: rule.Principal, Actions: g.Actions}, t.id, 0)
		}
	}
	if changed {
		ev.setGrants(p, grants)
	}
}

// grantFor is the grant a rule makes on an entity, reporting what it leaves
// out; false when it grants nothing there.
func (ev *evaluation) grantFor(rule GrantRule, t target) (config.GrantConfig, bool) {
	e := ev.entity(t)
	actions := slices.Clone(rule.Actions)
	if len(e.PrimaryKey) == 0 && len(e.UniqueKeys) == 0 {
		dropped := slices.DeleteFunc(slices.Clone(actions), func(a string) bool { return !slices.Contains(writes, a) })
		if len(dropped) > 0 {
			actions = slices.DeleteFunc(actions, func(a string) bool { return slices.Contains(writes, a) })
			ev.except(Exception{Kind: ExceptionNoKey, Ref: t.row.Ref, Entity: t.id,
				Detail: strings.Join(dropped, ",")})
		}
	}
	if len(actions) == 0 {
		return config.GrantConfig{}, false
	}
	g := config.GrantConfig{Entity: t.id, Actions: actions, Rows: rule.Rows}
	if len(rule.Fields) > 0 {
		patterns := compileGlobs(rule.Fields)
		var fields []string
		for _, f := range e.Fields {
			if !f.Exclude && patterns.match(f.Name) {
				fields = append(fields, f.Name)
			}
		}
		if len(fields) == 0 {
			ev.except(Exception{Kind: ExceptionNoFields, Ref: t.row.Ref, Entity: t.id,
				Detail: rule.Principal})
			return config.GrantConfig{}, false
		}
		g.Fields = &config.FieldACLConfig{Read: fields}
		if slices.ContainsFunc(actions, func(a string) bool { return a == "create" || a == "update" }) {
			g.Fields.Write = fields
		}
	}
	ev.checkGrant(rule, t, actions)
	return g, true
}

func (ev *evaluation) checkGrant(rule GrantRule, t target, actions []string) {
	if ev.in.Denied != nil && !t.new {
		for _, a := range actions {
			if why, denied := ev.in.Denied(t.id, a); denied {
				r := reason.Compose(reason.PlanActionDenied, map[string]reason.Reason{"cause": why}, "action", a)
				ev.except(Exception{Kind: ExceptionDenied, Ref: t.row.Ref, Entity: t.id, Detail: r.Text, Reason: r})
			}
		}
	}
	if ev.in.Review && t.new {
		ev.except(Exception{Kind: ExceptionReview, Ref: t.row.Ref, Entity: t.id, Detail: rule.Principal})
	}
}

// mergeGrant merges g into the grant among grants (at positions on, those on
// g's entity) with the same fields and rows, adding the actions it lacks. It
// returns where, or -1 when no grant matches, and whether actions were added.
func mergeGrant(grants []config.GrantConfig, on []int, g config.GrantConfig) (int, bool) {
	for _, i := range on {
		have := grants[i]
		if !sameFields(have.Fields, g.Fields) || !sameRows(have.Rows, g.Rows) {
			continue
		}
		missing := slices.DeleteFunc(
			slices.Clone(g.Actions),
			func(a string) bool { return slices.Contains(have.Actions, a) },
		)
		if len(missing) > 0 {
			grants[i].Actions = append(slices.Clone(have.Actions), missing...)
		}
		return i, len(missing) > 0
	}
	return -1, false
}

// sameFields reports whether a and b name the same fields: none (every
// field), or the same lists, an empty list the same as none written.
func sameFields(a, b *config.FieldACLConfig) bool {
	if a == nil || b == nil {
		return a == b
	}
	return slices.Equal(a.Read, b.Read) && slices.Equal(a.Write, b.Write)
}

// sameRows reports whether a and b filter the same rows, compared as JSON:
// a filter decoded from YAML holds 7 where one decoded from JSON (a plan)
// holds 7.0. No filter and an empty one are the same.
func sameRows(a, b config.FilterConfig) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}
