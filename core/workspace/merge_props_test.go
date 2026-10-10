package workspace

import (
	"fmt"
	"slices"
	"sort"
	"testing"
)

// Property tests: random configuration items edited at random on two sides,
// merged, and checked leaf by leaf against the merge rules. Seeded, so a
// failure names a seed that reproduces it.

type rng struct{ s uint32 }

func (r *rng) next() float64 {
	r.s = r.s*1664525 + 1013904223
	return float64(r.s) / (1 << 32)
}

func (r *rng) chance(p float64) bool { return r.next() < p }

func pick[T any](r *rng, xs []T) T { return xs[int(r.next()*float64(len(xs)))] }

var (
	propActions = []any{"READ", "AGGREGATE", "CREATE", "UPDATE", "DELETE"}
	propTexts   = []any{"", "a", "b", "c", Missing}
)

func setMaybe(m map[string]any, k string, v any) {
	if IsMissing(v) {
		delete(m, k)
	} else {
		m[k] = v
	}
}

func propField(r *rng, name string) map[string]any {
	f := map[string]any{"name": name, "exclude": r.chance(0.3)}
	if r.chance(0.5) {
		f["mask"] = pick(r, []any{"email", "phone", ""})
	}
	if r.chance(0.3) {
		setMaybe(f, "alias", pick(r, propTexts))
	}
	return f
}

func propGrant(r *rng, entity string) map[string]any {
	acts := []any{}
	for _, a := range propActions {
		if r.chance(0.5) {
			acts = append(acts, a)
		}
	}
	return map[string]any{"entity": entity, "actions": acts}
}

// propItem is a random item shaped like an entity with grants.
func propItem(r *rng) any {
	fields := []any{}
	for _, n := range []string{"id", "email", "phone", "region", "amount"} {
		if r.chance(0.7) {
			fields = append(fields, propField(r, n))
		}
	}
	grants := []any{}
	for _, e := range []string{"a", "b", "a"} {
		if r.chance(0.6) {
			grants = append(grants, propGrant(r, e))
		}
	}
	item := map[string]any{"name": "orders", "fields": fields, "grants": grants,
		"mcp": map[string]any{"dmlTools": r.chance(0.5)}}
	setMaybe(item, "description", pick(r, propTexts))
	if r.chance(0.5) {
		item["tenantPolicy"] = map[string]any{"op": "eq", "field": "tenant_id", "value": pick(r, []any{1.0, 2.0})}
	}
	return item
}

func clone(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, el := range x {
			out[k] = clone(el)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, el := range x {
			out[i] = clone(el)
		}
		return out
	}
	return v
}

func names(list any) []string {
	var out []string
	l, _ := list.([]any)
	for _, el := range l {
		if m, ok := el.(map[string]any); ok {
			if n, ok := m["name"].(string); ok {
				out = append(out, n)
			}
		}
	}
	return out
}

// propEdit applies a few random edits, the way an administrator would.
func propEdit(r *rng, base any) any {
	v := clone(base)
	for n := 1 + int(r.next()*3); n > 0; n-- {
		o := v.(map[string]any)
		fields := names(o["fields"])
		grants, _ := o["grants"].([]any)
		fieldAt := func(rest ...Step) Path {
			return append(Path{{Field: "fields"}, {Element: pick(r, fields)}}, rest...)
		}
		switch int(r.next() * 9) {
		case 0:
			setMaybe(o, "description", pick(r, propTexts))
		case 1:
			if len(fields) > 0 {
				v = schema.SetAt(v, fieldAt(Step{Field: "mask"}), pick(r, []any{"email", "secret", Missing}))
			}
		case 2:
			if len(fields) > 0 {
				v = schema.SetAt(v, fieldAt(), Missing)
			}
		case 3:
			key := pick(r, []string{"tax", "note", "email"})
			v = schema.SetAt(v, Path{{Field: "fields"}, {Element: key}}, propField(r, key))
		case 4:
			if len(fields) > 0 {
				v = schema.SetAt(v, fieldAt(Step{Field: "exclude"}), r.chance(0.5))
			}
		case 5:
			o["grants"] = append(slices.Clone(grants), propGrant(r, pick(r, []string{"a", "b", "c"})))
		case 6:
			if len(grants) > 0 {
				i := int(r.next() * float64(len(grants)))
				o["grants"] = slices.Delete(slices.Clone(grants), i, i+1)
			}
		case 7:
			o["mcp"] = map[string]any{"dmlTools": r.chance(0.5)}
		default:
			if r.chance(0.5) {
				delete(o, "tenantPolicy")
			} else {
				o["tenantPolicy"] = map[string]any{"op": "eq", "field": "tenant_id", "value": pick(r, []any{1.0, 2.0, 3.0})}
			}
		}
	}
	return v
}

// listKeys are the keys of a list the merge treats element by element, in
// order; false for any other list.
func listKeys(list []any, p string) ([]string, bool) {
	var keys []string
	for _, el := range list {
		key, ok := schema.elementKey(el, p)
		if !ok {
			return nil, false
		}
		if !slices.Contains(keys, key) {
			keys = append(keys, key)
		}
	}
	return keys, true
}

// leaves are every path to a value the merge does not descend into, in any of values.
func leaves(values []any, path Path, out map[string]Path) {
	var defined []any
	for _, v := range values {
		if at := schema.GetAt(v, path); !IsMissing(at) {
			defined = append(defined, at)
		}
	}
	if len(path) > 0 && path[len(path)-1].Field == "" && slices.ContainsFunc(defined, isGroup) {
		out[fmt.Sprint(path)] = path
		return
	}
	allObjects, allLists, keys := len(defined) > 0, len(defined) > 0, map[string]bool{}
	var listOrder []string
	for _, v := range defined {
		if m, ok := object(v); ok {
			for k := range m {
				keys[k] = true
			}
		} else {
			allObjects = false
		}
		l, ok := v.([]any)
		ks, keyed := listKeys(l, path.Pattern())
		if !ok || !keyed {
			allLists = false
		}
		listOrder = append(listOrder, ks...)
	}
	switch {
	case allObjects:
		for k := range keys {
			leaves(values, append(slices.Clone(path), Step{Field: k}), out)
		}
	case allLists:
		for _, k := range listOrder {
			leaves(values, append(slices.Clone(path), Step{Element: k}), out)
		}
	default:
		out[fmt.Sprint(path)] = path
	}
}

func checkMerge(t *testing.T, seed uint32) {
	r := &rng{seed}
	base := propItem(r)
	local, next := base, base
	if !r.chance(0.1) {
		local = propEdit(r, base)
	}
	if !r.chance(0.1) {
		next = propEdit(r, base)
	}
	value, conflicts := schema.Merge(base, local, next, nil)
	where := fmt.Sprintf("seed %d", seed)
	for _, c := range conflicts {
		// A conflict holds both sides as they are, differing, and keeps mine.
		if !schema.sameAt(c.Path, c.Mine, schema.GetAt(local, c.Path)) ||
			!schema.sameAt(c.Path, c.Theirs, schema.GetAt(next, c.Path)) || schema.sameAt(c.Path, c.Mine, c.Theirs) ||
			!schema.sameAt(c.Path, schema.GetAt(value, c.Path), c.Mine) {
			t.Fatalf("%s: conflict %+v", where, c)
		}
	}
	checkLeaves(t, where, [3]any{base, local, next}, value, conflicts)
	// Taking theirs everywhere leaves no local value that changed the same place.
	theirs := value
	for _, c := range conflicts {
		theirs = schema.SetAt(theirs, c.Path, c.Theirs)
	}
	for _, c := range conflicts {
		if !schema.sameAt(c.Path, schema.GetAt(theirs, c.Path), schema.GetAt(next, c.Path)) {
			t.Fatalf("%s: taking theirs at %s", where, c.Path)
		}
	}
	// Symmetric: swapping the sides swaps mine and theirs.
	_, swapped := schema.Merge(base, next, local, nil)
	if !slices.Equal(conflictPaths(swapped), conflictPaths(conflicts)) {
		t.Fatalf("%s: swapped %v, %v", where, conflictPaths(swapped), conflictPaths(conflicts))
	}
}

// checkLeaves checks every value the merge does not descend into: a side's
// change survives unless the other changed the same value, a conflict then.
func checkLeaves(t *testing.T, where string, sides [3]any, value any, conflicts []ValueConflict) {
	t.Helper()
	base, local, next := sides[0], sides[1], sides[2]
	inConflict := func(p Path) bool {
		return slices.ContainsFunc(conflicts, func(c ValueConflict) bool { return p.within(c.Path) })
	}
	all := map[string]Path{}
	leaves([]any{base, local, next}, nil, all)
	for _, p := range all {
		if inConflict(p) {
			continue
		}
		b, l, n := schema.GetAt(base, p), schema.GetAt(local, p), schema.GetAt(next, p)
		// A side's change survives unless the other changed the same value.
		want := l
		if schema.sameAt(p, l, b) {
			want = n
		}
		if !schema.sameAt(p, schema.GetAt(value, p), want) {
			t.Fatalf("%s at %s: %s, want %s", where, p, show(schema.GetAt(value, p)), show(want))
		}
		if !schema.sameAt(p, l, b) && !schema.sameAt(p, n, b) && !schema.sameAt(p, l, n) {
			t.Fatalf("%s: an unreported conflict at %s", where, p)
		}
	}
}

func conflictPaths(cs []ValueConflict) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Path.String())
	}
	sort.Strings(out)
	return out
}

func TestMergeProperties(t *testing.T) {
	for seed := uint32(1); seed <= 3000; seed++ {
		checkMerge(t, seed)
	}
}

// Where one side did not change, the merge is the other side.
func TestMergeIdentity(t *testing.T) {
	for seed := uint32(1); seed <= 300; seed++ {
		r := &rng{seed}
		base := propItem(r)
		changed := propEdit(r, base)
		for _, sides := range [][2]any{{base, changed}, {changed, base}} {
			if v, cs := schema.Merge(base, sides[0], sides[1], nil); !schema.Same(v, changed) || len(cs) != 0 {
				t.Fatalf("seed %d: %s %v", seed, show(v), cs)
			}
		}
		if _, cs := schema.Merge(base, changed, clone(changed), nil); len(cs) != 0 {
			t.Fatalf("seed %d: %v", seed, cs)
		}
	}
}
