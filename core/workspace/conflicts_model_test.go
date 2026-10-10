package workspace

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// Model-based test of a workspace across publishes: random sequences of
// local edits, publishes by another administrator (each followed by an
// update, Replay and Carry) and conflict choices, with conflicts left open
// across updates and edited after they arise. After every step the
// invariants below must hold; a failure names the seed and step.

var (
	modelNames    = []string{"analyst", "finance"}
	modelEntities = []string{"orders", "refunds"}
	modelActions  = []any{"READ", "AGGREGATE", "CREATE"}
	modelTexts    = []any{"", "a", "b", "c"}
)

type role = map[string]any

func roleItems(roles []role) Snapshot {
	var out Snapshot
	for _, r := range roles {
		out = append(out, Item{Ref: Ref{Kind: "role", Key: r["name"].(string)}, Value: clone(r)})
	}
	return out
}

func rolesOf(s Snapshot) []role {
	var out []role
	for _, it := range s {
		out = append(out, it.Value.(map[string]any))
	}
	return out
}

func grantsOf(r role) []any { g, _ := r["grants"].([]any); return g }

// editRoles is a random edit of roles, as either administrator might make.
func editRoles(rnd *rng, roles []role) []role {
	out := rolesOf(roleItems(roles)) // a copy
	var r role
	if len(out) > 0 {
		r = pick(rnd, out)
	}
	switch int(rnd.next() * 7) {
	case 0:
		name := pick(rnd, modelNames)
		if !slices.ContainsFunc(out, func(x role) bool { return x["name"] == name }) {
			out = append(out, role{"name": name, "description": pick(rnd, modelTexts), "grants": []any{}})
		}
	case 1:
		if r != nil {
			return slices.DeleteFunc(out, func(x role) bool { return x["name"] == r["name"] })
		}
	case 2:
		if r != nil {
			r["description"] = pick(rnd, modelTexts)
		}
	case 3:
		if r != nil {
			r["grants"] = append(grantsOf(r), map[string]any{"entity": pick(rnd, modelEntities),
				"actions": []any{pick(rnd, modelActions)}})
		}
	case 4:
		if g := grantsOf(r); len(g) > 0 {
			i := int(rnd.next() * float64(len(g)))
			r["grants"] = slices.Delete(slices.Clone(g), i, i+1)
		}
	default:
		if g := grantsOf(r); len(g) > 0 {
			acts := []any{}
			for _, a := range modelActions {
				if rnd.next() < 0.5 {
					acts = append(acts, a)
				}
			}
			pick(rnd, g).(map[string]any)["actions"] = acts
		}
	}
	return out
}

// units are the values of a role list the merge compares separately: a
// role's existence, its description, and its grants on each entity.
func units(roles []role) map[string]any {
	out := map[string]any{}
	for _, r := range roles {
		name := r["name"].(string)
		out[name+"|role"] = true
		out[name+"|description"] = r["description"]
		for _, e := range modelEntities {
			var gs []string
			for _, g := range grantsOf(r) {
				if gm := g.(map[string]any); gm["entity"] == e {
					gs = append(gs, schema.Canonical(sorted(gm["actions"])))
				}
			}
			if gs != nil {
				out[name+"|grants:"+e] = strings.Join(gs, ";")
			}
		}
	}
	return out
}

func sorted(v any) any {
	l, _ := v.([]any)
	s := make([]string, 0, len(l))
	for _, x := range l {
		s = append(s, x.(string))
	}
	slices.Sort(s)
	return s
}

func changedUnits(a, b []role) map[string]bool {
	ua, ub := units(a), units(b)
	out := map[string]bool{}
	for k := range ua {
		if !schema.Same(ua[k], ub[k]) {
			out[k] = true
		}
	}
	for k := range ub {
		if _, ok := ua[k]; !ok {
			out[k] = true
		}
	}
	return out
}

// covered reports whether an open conflict covers a unit: on its role, at or
// around its path.
func covered(open []Open, unit string) bool {
	name, what, _ := strings.Cut(unit, "|")
	return slices.ContainsFunc(open, func(c Open) bool {
		if c.Ref.Key != name {
			return false
		}
		if len(c.Path) == 0 {
			return true
		}
		switch {
		case what == "description":
			return c.Path[0].Field == "description"
		case strings.HasPrefix(what, "grants:"):
			return c.Path[0].Field == "grants" && (len(c.Path) == 1 || c.Path[1].Element == what[7:])
		}
		return false
	})
}

type model struct {
	t                 *testing.T
	rnd               *rng
	base, local       Snapshot
	conflicts         []Conflict
	id                int
	edited, contested map[string]bool
	trace             []string
}

func (m *model) editLocally() {
	before := rolesOf(m.local)
	m.local = roleItems(editRoles(m.rnd, before))
	after := units(rolesOf(m.local))
	for u := range changedUnits(before, rolesOf(m.local)) {
		m.edited[u] = true
		// Removing a role or a grant here is an edit on the newer revision:
		// what it held is no longer contested.
		if _, ok := after[u]; !ok {
			delete(m.contested, u)
			if prefix, isRole := strings.CutSuffix(u, "role"); isRole {
				for c := range m.contested {
					if strings.HasPrefix(c, prefix) {
						delete(m.contested, c)
					}
				}
			}
		}
	}
	m.trace = append(m.trace, "local "+show(m.local))
}

func (m *model) publish() {
	published := roleItems(editRoles(m.rnd, rolesOf(m.base)))
	for u := range changedUnits(rolesOf(m.base), rolesOf(published)) {
		if m.edited[u] {
			m.contested[u] = true
		}
	}
	m.id++
	src := Source{Kind: SourcePublished, ID: fmt.Sprint(m.id)}
	now, found := schema.Replay(m.base, m.local, published, src, false)
	m.conflicts = schema.Carry(m.conflicts, found, now, published, &src)
	m.base, m.local = published, now
	m.trace = append(m.trace, fmt.Sprintf("publish %d %s -> %s", m.id, show(published), show(m.local)))
}

func (m *model) resolve() {
	open := schema.OpenConflicts(m.local, m.base, m.conflicts)
	if len(open) == 0 {
		return
	}
	c := pick(m.rnd, open)
	theirs := m.rnd.next() < 0.5
	var ok bool
	m.local, m.conflicts, ok = schema.Resolve(m.local, m.base, m.conflicts, c.ID, theirs)
	if !ok {
		m.t.Fatalf("%s: an open conflict did not resolve", m.at())
	}
	for _, o := range schema.OpenConflicts(m.local, m.base, m.conflicts) {
		if o.ID == c.ID {
			m.t.Fatalf("%s: a resolved conflict is still open", m.at())
		}
	}
	for u := range m.contested {
		if covered([]Open{c}, u) {
			delete(m.contested, u)
		}
	}
	want := c.Mine
	if theirs {
		want = c.TheirsValue
	}
	if !schema.sameAt(c.Path, schema.GetAt(m.local.Get(c.Ref), c.Path), want) {
		m.t.Fatalf("%s: the choice did not take effect", m.at())
	}
	m.trace = append(m.trace, fmt.Sprintf("resolve %s %s theirs=%v", c.Ref.Key, c.Path, theirs))
}

func (m *model) at() string { return strings.Join(m.trace, "\n") }

func (m *model) check() {
	open := schema.OpenConflicts(m.local, m.base, m.conflicts)
	// No silent overwrite: a value both changed that differs from what is
	// published is under an open conflict.
	diverged := changedUnits(rolesOf(m.local), rolesOf(m.base))
	for u := range m.edited {
		if !diverged[u] {
			delete(m.edited, u)
			delete(m.contested, u)
		}
	}
	for u := range m.contested {
		if !covered(open, u) {
			m.t.Fatalf("%s\n%s was changed on both sides and is not under a conflict", m.at(), u)
		}
	}
	for _, c := range open {
		// Mine is what the workspace holds, theirs what is published now; they differ.
		if !schema.sameAt(c.Path, c.Mine, schema.GetAt(m.local.Get(c.Ref), c.Path)) ||
			!schema.sameAt(c.Path, c.TheirsValue, schema.GetAt(m.base.Get(c.Ref), c.Path)) ||
			schema.sameAt(c.Path, c.Mine, c.TheirsValue) {
			m.t.Fatalf("%s\nconflict %+v", m.at(), c)
		}
		for _, o := range open {
			if o.ID != c.ID && within(c.Conflict, o.Conflict) {
				m.t.Fatalf("%s\nnested conflicts %+v %+v", m.at(), c, o)
			}
		}
	}
}

func TestConflictsAcrossPublishes(t *testing.T) {
	for seed := uint32(1); seed <= 1500; seed++ {
		m := &model{t: t, rnd: &rng{seed}, edited: map[string]bool{}, contested: map[string]bool{},
			trace: []string{fmt.Sprintf("seed %d", seed)}}
		for range 40 {
			switch op := m.rnd.next(); {
			case op < 0.5:
				m.editLocally()
			case op < 0.85:
				m.publish()
			default:
				m.resolve()
			}
			m.check()
		}
		// Whatever is left can always be resolved, and then nothing blocks.
		for range 50 {
			open := schema.OpenConflicts(m.local, m.base, m.conflicts)
			if len(open) == 0 {
				break
			}
			m.local, m.conflicts, _ = schema.Resolve(m.local, m.base, m.conflicts, open[0].ID, m.rnd.next() < 0.5)
		}
		if open := schema.OpenConflicts(m.local, m.base, m.conflicts); len(open) > 0 {
			t.Fatalf("seed %d: unresolvable conflicts %+v", seed, open)
		}
	}
}
