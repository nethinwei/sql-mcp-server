package configapp

import (
	"slices"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/workspace"
	"github.com/nethinwei/sql-mcp-server/x/configrefs"
)

// reconcile is the configuration's workspaceops.Reconciler: what a write
// (before to after) changes in the items it did not write, as the rules of
// configrefs and of roles have it (see docs/design/editing.md).
//
//   - An existing reference to an entity (a grant's entity, a relationship's
//     target, a procedure's affects) keeps naming the entity it named: it
//     follows a rename or move, is qualified when it became ambiguous, and
//     goes with a removed entity. A reference the write itself made is left
//     as written.
//   - An entity renamed without a source keeps reading its table.
//   - Fields removed from an entity leave the field lists of grants on it.
//   - A renamed or removed role is renamed or removed where users and the
//     entity-level (legacy) access name it.
//
// It edits only those values of an item's JSON, so items keep their shape.
func reconcile(before, after workspace.Snapshot, renames map[workspace.Ref]workspace.Ref) (
	workspace.Snapshot, error,
) {
	r := reconciliation{before: before, beforeAt: before.Index(), renames: renames, moved: map[string]string{},
		roles: map[string]string{}}
	for to, from := range renames {
		switch {
		case to.Kind == ItemEntity && from.Kind == ItemEntity:
			r.moved[from.Key] = to.Key
		case to.Kind == ItemRole && from.Kind == ItemRole:
			r.roles[from.Key] = to.Key
		}
	}
	// An entity that a renamed one replaced is removed: what named it must
	// not name the entity now holding its ID.
	for to := range renames {
		if _, movedAway := r.moved[to.Key]; to.Kind == ItemEntity && !movedAway &&
			!workspace.IsMissing(before.Get(to)) {
			r.moved[to.Key] = ""
		}
	}
	afterAt := after.Index()
	for _, it := range before {
		if _, kept := afterAt[it.Ref]; it.Kind == ItemRole && !kept {
			if _, renamed := r.roles[it.Key]; !renamed {
				r.roles[it.Key] = "" // removed
			}
		}
	}
	r.beforeIdx, r.afterIdx = configrefs.NewIndex(entitiesOf(before)), configrefs.NewIndex(entitiesOf(after))
	changed := map[workspace.Ref]any{}
	for _, it := range after {
		v, ok := object(it.Value)
		if !ok {
			continue
		}
		next := cloneJSON(v).(map[string]any)
		if r.item(it.Ref, next) {
			changed[it.Ref] = next
		}
	}
	return r.pruneFields(after.Apply(changed, nil)), nil
}

type reconciliation struct {
	before   workspace.Snapshot
	beforeAt map[workspace.Ref]int
	renames  map[workspace.Ref]workspace.Ref
	// moved maps renamed entities' IDs to their new ones and replaced ones
	// to ""; roles maps renamed roles to their new names and removed ones to
	// "".
	moved               map[string]string
	roles               map[string]string
	beforeIdx, afterIdx configrefs.Index
}

// previous is the item ref was (where it was renamed from), or nil.
func (r reconciliation) previous(ref workspace.Ref) map[string]any {
	if from, ok := r.renames[ref]; ok {
		ref = from
	}
	if i, ok := r.beforeAt[ref]; ok {
		v, _ := object(r.before[i].Value)
		return v
	}
	return nil
}

// item reconciles v, the value of ref, in place; it reports a change.
func (r reconciliation) item(ref workspace.Ref, v map[string]any) bool {
	prev := r.previous(ref)
	switch ref.Kind {
	case ItemRole:
		return r.grants(v, prev)
	case ItemUser:
		changed := r.grants(v, prev)
		if roles, ok := v["roles"].([]any); ok {
			if next, renamed := r.roleNames(roles); renamed {
				v["roles"], changed = next, true
			}
		}
		return changed
	case ItemEntity:
		changed := r.keepSource(ref, v, prev)
		changed = r.relationships(v, prev) || changed
		changed = r.affects(v, prev) || changed
		return r.legacyRoles(v) || changed
	}
	return false
}

// retarget is what the existing reference ref becomes; keep is false when
// it goes.
func (r reconciliation) retarget(ref string) (string, bool) {
	return configrefs.Retarget(ref, r.beforeIdx, r.afterIdx, r.moved)
}

func (r reconciliation) grants(v, prev map[string]any) bool {
	list, ok := v["grants"].([]any)
	if !ok {
		return false
	}
	existing := map[string]bool{}
	for _, g := range listAt(prev, "grants") {
		existing[text(g, "entity")] = true
	}
	changed := false
	next := make([]any, 0, len(list))
	for _, g := range list {
		ref := text(g, "entity")
		if !existing[ref] {
			next = append(next, g)
			continue
		}
		to, keep := r.retarget(ref)
		switch {
		case !keep:
			changed = true
		case to != ref:
			g.(map[string]any)["entity"], changed = to, true
			next = append(next, g)
		default:
			next = append(next, g)
		}
	}
	v["grants"] = next
	return changed
}

func (r reconciliation) relationships(v, prev map[string]any) bool {
	list, ok := v["relationships"].([]any)
	if !ok {
		return false
	}
	existing := map[string]string{}
	for _, rel := range listAt(prev, "relationships") {
		existing[text(rel, "name")] = text(rel, "target")
	}
	changed := false
	next := make([]any, 0, len(list))
	for _, rel := range list {
		target := text(rel, "target")
		if was, ok := existing[text(rel, "name")]; !ok || was != target {
			next = append(next, rel)
			continue
		}
		to, keep := r.retarget(target)
		switch {
		case !keep:
			changed = true
		case to != target:
			rel.(map[string]any)["target"], changed = to, true
			next = append(next, rel)
		default:
			next = append(next, rel)
		}
	}
	v["relationships"] = next
	return changed
}

func (r reconciliation) affects(v, prev map[string]any) bool {
	list, ok := v["affects"].([]any)
	if !ok {
		return false
	}
	existing := listAt(prev, "affects")
	changed := false
	next := make([]any, 0, len(list))
	for _, a := range list {
		ref, _ := a.(string)
		if !slices.Contains(existing, a) {
			next = append(next, a)
			continue
		}
		to, keep := r.retarget(ref)
		if keep {
			next = append(next, to)
		}
		changed = changed || !keep || to != ref
	}
	v["affects"] = next
	return changed
}

// keepSource has an entity renamed without a source read the table it read.
func (r reconciliation) keepSource(ref workspace.Ref, v, prev map[string]any) bool {
	if _, renamed := r.renames[ref]; !renamed || prev == nil || text(v, "source") != "" ||
		text(v, "name") == text(prev, "name") {
		return false
	}
	source := text(prev, "source")
	if source == "" {
		source = text(prev, "name")
	}
	v["source"] = source
	return true
}

// roleNames are names with renamed roles renamed and removed ones dropped.
func (r reconciliation) roleNames(names []any) ([]any, bool) {
	changed := false
	next := make([]any, 0, len(names))
	for _, n := range names {
		name, _ := n.(string)
		to, ok := r.roles[name]
		switch {
		case !ok:
			next = append(next, n)
		case to != "":
			next, changed = append(next, to), true
		default:
			changed = true
		}
	}
	return next, changed
}

// legacyRoles renames and removes roles in an entity's legacy access: the
// role lists by action, and the roles fieldACL and rowPolicies are keyed by.
func (r reconciliation) legacyRoles(v map[string]any) bool {
	legacy, ok := v["legacyAccess"].(map[string]any)
	if !ok || len(r.roles) == 0 {
		return false
	}
	changed := false
	if byAction, ok := legacy["roles"].(map[string]any); ok {
		for action, names := range byAction {
			if list, ok := names.([]any); ok {
				if next, renamed := r.roleNames(list); renamed {
					byAction[action], changed = next, true
				}
			}
		}
	}
	for _, key := range []string{"fieldACL", "rowPolicies"} {
		byRole, ok := legacy[key].(map[string]any)
		if !ok {
			continue
		}
		// Every renamed role's value is taken out first and then put under
		// its new name, so roles that swapped names (or a chain of renames)
		// move together, in whatever order the map lists them.
		moved := map[string]any{}
		for from := range r.roles {
			if value, ok := byRole[from]; ok {
				moved[from] = value
				delete(byRole, from)
				changed = true
			}
		}
		for from, value := range moved {
			if to := r.roles[from]; to != "" {
				byRole[to] = value
			}
		}
	}
	return changed
}

// pruneFields drops the fields entities lost from the field lists of the
// grants on them.
func (r reconciliation) pruneFields(out workspace.Snapshot) workspace.Snapshot {
	removed := map[string][]any{} // by entity ID after
	for _, it := range out {
		if it.Kind != ItemEntity {
			continue
		}
		v, ok := object(it.Value)
		prev := r.previous(it.Ref)
		if !ok || prev == nil {
			continue
		}
		now := fieldNames(v)
		for _, f := range fieldNames(prev) {
			if !slices.Contains(now, f) {
				removed[it.Key] = append(removed[it.Key], f)
			}
		}
	}
	if len(removed) == 0 {
		return out
	}
	pruned := map[workspace.Ref]any{}
	for _, it := range out {
		if it.Kind != ItemRole && it.Kind != ItemUser {
			continue
		}
		v, ok := object(it.Value)
		if !ok {
			continue
		}
		next := cloneJSON(v).(map[string]any)
		if r.pruneGrants(next, removed) {
			pruned[it.Ref] = next
		}
	}
	return out.Apply(pruned, nil)
}

func (r reconciliation) pruneGrants(v map[string]any, removed map[string][]any) bool {
	changed := false
	for _, g := range listAt(v, "grants") {
		grant, ok := g.(map[string]any)
		if !ok {
			continue
		}
		id, ok := r.afterIdx.IDOf(text(grant, "entity"))
		if !ok || removed[id] == nil {
			continue
		}
		for _, key := range []string{"readFields", "writeFields"} {
			fields, ok := grant[key].([]any)
			if !ok {
				continue
			}
			kept := slices.DeleteFunc(slices.Clone(fields), func(f any) bool { return slices.Contains(removed[id], f) })
			if len(kept) != len(fields) {
				grant[key], changed = kept, true
			}
		}
	}
	return changed
}

// entitiesOf are the entities of items, by name, datasource and schema.
func entitiesOf(items workspace.Snapshot) []config.EntityConfig {
	var out []config.EntityConfig
	for _, it := range items {
		if v, ok := object(it.Value); ok && it.Kind == ItemEntity {
			out = append(out, config.EntityConfig{
				Name: text(v, "name"), DataSource: text(v, "datasource"), Schema: text(v, "schema"),
			})
		}
	}
	return out
}

func fieldNames(entity map[string]any) []any {
	var out []any
	for _, f := range listAt(entity, "fields") {
		if m, ok := f.(map[string]any); ok {
			out = append(out, m["name"])
		}
	}
	return out
}

func object(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

// listAt is the list at key of v (nil when absent).
func listAt(v map[string]any, key string) []any {
	list, _ := v[key].([]any)
	return list
}

// text is the string at key of v ("" when absent).
func text(v any, key string) string {
	m, _ := v.(map[string]any)
	s, _ := m[key].(string)
	return s
}

// cloneJSON deep-copies decoded JSON, so editing the copy leaves the
// snapshot it came from as it was.
func cloneJSON(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = cloneJSON(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = cloneJSON(val)
		}
		return out
	}
	return v
}
