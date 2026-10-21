package configapp

import (
	"maps"
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
//   - The budget.roles and budget.users entries of a renamed or removed role
//     or user follow it.
//
// It edits only those values of an item's JSON, so items keep their shape.
func reconcile(before, after workspace.Snapshot, renames map[workspace.Ref]workspace.Ref) (
	workspace.Snapshot, error,
) {
	r := reconciliation{before: before, beforeAt: before.Index(), renames: renames, moved: map[string]string{},
		roles: map[string]string{}, users: map[string]string{}}
	for to, from := range renames {
		switch {
		case to.Kind == ItemEntity && from.Kind == ItemEntity:
			r.moved[from.Key] = to.Key
		case to.Kind == ItemRole && from.Kind == ItemRole:
			r.roles[from.Key] = to.Key
		case to.Kind == ItemUser && from.Kind == ItemUser:
			r.users[from.Key] = to.Key
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
	r.removed(after)
	if len(r.moved) == 0 && len(r.roles) == 0 && len(r.users) == 0 && sameEntities(before, after) {
		// Every reference names what it named: only removed fields remain.
		return r.pruneFields(after), nil
	}
	r.beforeIdx, r.afterIdx = configrefs.NewIndex(entitiesOf(before)), configrefs.NewIndex(entitiesOf(after))
	changed := map[workspace.Ref]any{}
	for _, it := range after {
		v, ok := object(it.Value)
		if !ok {
			continue
		}
		next := editable(it.Kind, v)
		if r.item(it.Ref, next) {
			changed[it.Ref] = next
		}
	}
	return r.pruneFields(after.Apply(changed, nil)), nil
}

// removed records the roles and users the write removed (not renamed).
func (r reconciliation) removed(after workspace.Snapshot) {
	afterAt := after.Index()
	for _, it := range r.before {
		if _, kept := afterAt[it.Ref]; kept {
			continue
		}
		for kind, names := range map[string]map[string]string{ItemRole: r.roles, ItemUser: r.users} {
			if _, renamed := names[it.Key]; it.Kind == kind && !renamed {
				names[it.Key] = "" // removed
			}
		}
	}
}

type reconciliation struct {
	before   workspace.Snapshot
	beforeAt map[workspace.Ref]int
	renames  map[workspace.Ref]workspace.Ref
	// moved maps renamed entities' IDs to their new ones and replaced ones
	// to ""; roles and users map renamed roles and users to their new names
	// and removed ones to "".
	moved               map[string]string
	roles, users        map[string]string
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
	case ItemSettings:
		return r.budgets(v)
	}
	return false
}

// budgets renames or removes the budget.roles and budget.users entries of
// the roles and users renamed or removed: a budget naming a user that is
// gone fails validation, and one naming a role that is gone applies to none.
func (r reconciliation) budgets(v map[string]any) bool {
	budget, ok := v["budget"].(map[string]any)
	if !ok {
		return false
	}
	changed := renameKeys(budget, "roles", r.roles)
	return renameKeys(budget, "users", r.users) || changed
}

// renameKeys moves the entries of section[key] named in names to their new
// names, or drops them for "". A moved entry replaces one the new name
// already has, as the role and user now holding the name are the renamed
// ones (and as legacyRoles moves entity-level access): the entries all move
// at once, so a chain (a to b, b to c) or a swap moves each whatever the
// order.
func renameKeys(section map[string]any, key string, names map[string]string) bool {
	byName, ok := section[key].(map[string]any)
	if !ok {
		return false
	}
	moving := map[string]any{}
	for from := range names {
		if limits, ok := byName[from]; ok {
			moving[from] = limits
			delete(byName, from)
		}
	}
	for _, from := range slices.Sorted(maps.Keys(moving)) {
		if to := names[from]; to != "" {
			byName[to] = moving[from]
		}
	}
	return len(moving) > 0
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

// roleNames are names with renamed roles renamed and removed ones dropped;
// a role renamed onto a name the list already has is listed once.
func (r reconciliation) roleNames(names []any) ([]any, bool) {
	changed := false
	next := make([]any, 0, len(names))
	renamed := map[string]bool{} // names a rename put in next
	for _, n := range names {
		name, _ := n.(string)
		to, ok := r.roles[name]
		switch {
		case !ok && !renamed[name]:
			next = append(next, n)
		case to != "" && !slices.Contains(next, any(to)):
			next, changed, renamed[to] = append(next, to), true, true
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
		if !ok || prev == nil || workspace.Identical(v, prev) {
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
	// Reconciling keeps every entity's ID: out resolves references as after.
	idx := configrefs.NewIndex(entitiesOf(out))
	pruned := map[workspace.Ref]any{}
	for _, it := range out {
		if it.Kind != ItemRole && it.Kind != ItemUser {
			continue
		}
		v, ok := object(it.Value)
		if !ok {
			continue
		}
		next := editable(it.Kind, v)
		if pruneGrants(idx, next, removed) {
			pruned[it.Ref] = next
		}
	}
	return out.Apply(pruned, nil)
}

func pruneGrants(idx configrefs.Index, v map[string]any, removed map[string][]any) bool {
	changed := false
	for _, g := range listAt(v, "grants") {
		grant, ok := g.(map[string]any)
		if !ok {
			continue
		}
		id, ok := idx.IDOf(text(grant, "entity"))
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
// sameEntities reports whether a and b hold the same entities (by ID): a
// reference then resolves alike in both.
func sameEntities(a, b workspace.Snapshot) bool {
	ids := func(s workspace.Snapshot) []string {
		var out []string
		for _, it := range s {
			if it.Kind == ItemEntity {
				out = append(out, it.Key)
			}
		}
		slices.Sort(out)
		return out
	}
	return slices.Equal(ids(a), ids(b))
}

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
// edited are the keys of an item, by kind, whose values reconciling edits
// in place (it sets others, as an entity's source, on the item itself).
var edited = map[string][]string{
	ItemRole:     {"grants"},
	ItemUser:     {"grants", "roles"},
	ItemEntity:   {"relationships", "affects", "legacyAccess"},
	ItemSettings: {"budget"},
}

// editable is a copy of v, an item of kind, that reconciling may edit
// without writing into v: the values it edits are copied deep, the others
// (an entity's fields, the bulk of the workspace) shared.
func editable(kind string, v map[string]any) map[string]any {
	out := maps.Clone(v)
	for _, key := range edited[kind] {
		if val, ok := out[key]; ok {
			out[key] = cloneJSON(val)
		}
	}
	return out
}

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
