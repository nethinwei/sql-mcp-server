package workspace

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"
)

// Values are decoded JSON (map[string]any, []any, string, float64, bool,
// nil); Missing stands for a value that is not there (a removed item, an
// absent key), which JSON null is not.
//
// Three-way merge of a value: objects merge key by key, and lists of keyed
// elements element by element (by the Schema's keys, e.g. fields by name,
// grants by the entity they name). Elements sharing a key (several grants on
// one entity) have no identity of their own, so they merge as one group:
// matching them by position would pair a grant with another once one side
// removes one. A value both sides changed differently is a conflict: the
// merged value keeps the local one, and nothing is overwritten silently.

type missing struct{}

// Missing is a value that is not there.
var Missing any = missing{}

// IsMissing reports whether v is Missing.
func IsMissing(v any) bool {
	_, ok := v.(missing)
	return ok
}

// Schema holds the rules the merge needs of the values it merges, by path
// from an item's root as Path.Pattern writes it ("fields[].mask": the mask
// of any element of fields). A rule applies at its path only, never to a
// property of the same name elsewhere.
type Schema struct {
	// Lists are the lists whose elements are keyed, by the pattern of their
	// elements ("fields[]"), with the property keying them ("name"); any
	// other list is one value.
	Lists map[string]string
	// Unset are the values whose zero (empty text, false, null) means unset,
	// equal to no value at all.
	Unset map[string]bool
}

// Step is a step into a value: an object property, or the element of a list
// with a key.
type Step struct {
	Field   string `json:"field,omitempty"`
	Element string `json:"element,omitempty"`
}

// Path is a sequence of steps; empty is the whole value.
type Path []Step

// Pattern is the path without its element keys, e.g. fields[].mask: the
// form Schema rules are written in.
func (p Path) Pattern() string {
	var b strings.Builder
	for _, s := range p {
		b.WriteString(patternStep(b.Len() == 0, s))
	}
	return b.String()
}

func patternStep(first bool, s Step) string {
	switch {
	case s.Element != "" || s.Field == "":
		return "[]"
	case first:
		return s.Field
	}
	return "." + s.Field
}

// field is the pattern of property k under pattern p.
func field(p, k string) string {
	if p == "" {
		return k
	}
	return p + "." + k
}

// String is a readable path, e.g. fields[email].mask.
func (p Path) String() string {
	var b strings.Builder
	for i, s := range p {
		switch {
		case s.Element != "" || s.Field == "":
			b.WriteString("[")
			b.WriteString(s.Element)
			b.WriteString("]")
		case i > 0:
			b.WriteString(".")
			b.WriteString(s.Field)
		default:
			b.WriteString(s.Field)
		}
	}
	return b.String()
}

// within reports whether p is q or under it.
func (p Path) within(q Path) bool { return len(p) >= len(q) && slices.Equal(p[:len(q)], q) }

// ValueConflict is a value both sides changed differently.
type ValueConflict struct {
	Path Path
	// Mine is the local value, kept; Missing when removed locally.
	Mine any
	// Theirs is the other side's value; Missing when removed there.
	Theirs any
}

func object(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

// normalize drops the values at Unset patterns that are zero, and Missing
// values; v is at pattern p.
func (s Schema) normalize(v any, p string) any {
	if s.Unset[p] && (v == "" || v == false || v == nil) {
		return Missing
	}
	switch x := v.(type) {
	case []any:
		out := make([]any, len(x))
		for i, el := range x {
			out[i] = s.normalize(el, p+"[]")
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, el := range x {
			if n := s.normalize(el, field(p, k)); !IsMissing(n) {
				out[k] = n
			}
		}
		return out
	}
	return v
}

// comparer compares values during one merge, diff or replay. It keeps the
// canonical JSON of the objects and lists it serialized, by their identity
// and pattern, so a subtree is serialized once however many of its ancestors
// compare it (each level of a merge compares the values under it): a merge
// costs the size of its inputs, not their size times their depth. The
// inputs of one operation are not written to while it runs. An entry holds
// its value: values built during the operation (the groups of a keyed list)
// may go, and their address must not name another value meanwhile.
type comparer struct {
	s    Schema
	memo map[memoKey]memoEntry
}

type memoEntry struct {
	v   any
	out string
}

// memoKey identifies a map or a list value at a pattern: a map by its
// pointer, a list by its first element's address and its length.
type memoKey struct {
	pattern string
	ptr     uintptr
	n       int
}

func (s Schema) comparer() *comparer { return &comparer{s: s, memo: map[memoKey]memoEntry{}} }

// canonicalAt is the canonical JSON of v at pattern p.
func (c *comparer) canonicalAt(p string, v any) string {
	key, memoized := memoKeyOf(p, v)
	if memoized {
		if e, ok := c.memo[key]; ok {
			return e.out
		}
	}
	n := c.s.normalize(v, p)
	if IsMissing(n) {
		n = nil
	}
	data, _ := json.Marshal(n)
	if memoized {
		c.memo[key] = memoEntry{v: v, out: string(data)}
	}
	return string(data)
}

func memoKeyOf(p string, v any) (memoKey, bool) {
	switch x := v.(type) {
	case map[string]any:
		if len(x) == 0 {
			return memoKey{}, false
		}
		return memoKey{pattern: p, ptr: reflect.ValueOf(x).Pointer(), n: len(x)}, true
	case []any:
		if len(x) == 0 {
			return memoKey{}, false
		}
		return memoKey{pattern: p, ptr: reflect.ValueOf(x).Pointer(), n: len(x)}, true
	}
	return memoKey{}, false
}

// Canonical is the canonical JSON of an item: keys sorted, unset values
// dropped.
func (s Schema) Canonical(v any) string { return s.comparer().canonicalAt("", v) }

// Same reports whether two items mean the same, regardless of key order or
// unset values.
func (s Schema) Same(a, b any) bool { return Identical(a, b) || s.Canonical(a) == s.Canonical(b) }

// Identical reports whether a and b are one map or list (values are never
// written to once built, so they mean the same): an item a write left alone
// compares without encoding it.
func Identical(a, b any) bool {
	ka, okA := memoKeyOf("", a)
	kb, okB := memoKeyOf("", b)
	return okA && okB && ka == kb
}

// sameAt compares two values at path.
func (c *comparer) sameAt(path Path, a, b any) bool {
	if n := len(path); n > 0 && (path[n-1].Element != "" || path[n-1].Field == "") {
		return c.sameElements(path, a, b)
	}
	if Identical(a, b) {
		return true
	}
	p := path.Pattern()
	return c.canonicalAt(p, a) == c.canonicalAt(p, b)
}

// sameElements compares two values at path, an element of a keyed list,
// either of which may be a group of the elements sharing its key.
func (c *comparer) sameElements(path Path, a, b any) bool {
	p := path.Pattern()
	canonical := func(v any) string {
		if group, ok := v.([]any); ok {
			return c.canonicalAt(strings.TrimSuffix(p, "[]"), group)
		}
		return c.canonicalAt(p, v)
	}
	return canonical(a) == canonical(b)
}

// elementKey is the key of el, an element of the list at pattern p.
func (s Schema) elementKey(el any, p string) (string, bool) {
	prop, keyed := s.Lists[p+"[]"]
	m, ok := object(el)
	if !keyed || !ok {
		return "", false
	}
	key, ok := m[prop].(string)
	return key, ok
}

// keyedList is a list of keyed elements, in order of first appearance;
// elements sharing a key map to the list of them (a group).
type keyedList struct {
	order []string
	byKey map[string]any
}

// keyed is v, a list at pattern p, by key; false when it is not a keyed list.
func (s Schema) keyed(v any, p string) (keyedList, bool) {
	list, ok := v.([]any)
	if !ok {
		return keyedList{}, false
	}
	k := keyedList{byKey: map[string]any{}}
	groups := map[string][]any{}
	for _, el := range list {
		key, ok := s.elementKey(el, p)
		if !ok {
			return keyedList{}, false
		}
		if _, seen := groups[key]; !seen {
			k.order = append(k.order, key)
		}
		groups[key] = append(groups[key], el)
	}
	for key, els := range groups {
		k.byKey[key] = any(els)
		if len(els) == 1 {
			k.byKey[key] = els[0]
		}
	}
	return k, true
}

func (k keyedList) get(key string) any {
	if v, ok := k.byKey[key]; ok {
		return v
	}
	return Missing
}

func get(m map[string]any, key string) any {
	if v, ok := m[key]; ok {
		return v
	}
	return Missing
}

// Merge merges base → local and base → next, the values at path.
func (s Schema) Merge(base, local, next any, path Path) (any, []ValueConflict) {
	return s.comparer().merge(base, local, next, path)
}

func (c *comparer) merge(base, local, next any, path Path) (any, []ValueConflict) {
	if c.sameAt(path, local, base) {
		return next, nil
	}
	if c.sameAt(path, next, base) || c.sameAt(path, next, local) {
		return local, nil
	}
	bo, okB := object(base)
	lo, okL := object(local)
	no, okN := object(next)
	if okB && okL && okN {
		return c.mergeObjects(bo, lo, no, path)
	}
	if IsMissing(base) {
		base = []any{}
	}
	p := path.Pattern()
	b, okB := c.s.keyed(base, p)
	l, okL := c.s.keyed(local, p)
	n, okN := c.s.keyed(next, p)
	if okB && okL && okN {
		return c.mergeLists(b, l, n, path)
	}
	return local, []ValueConflict{{Path: path, Mine: local, Theirs: next}}
}

func (c *comparer) mergeObjects(base, local, next map[string]any, path Path) (any, []ValueConflict) {
	value := map[string]any{}
	var conflicts []ValueConflict
	keys := map[string]bool{}
	for _, m := range []map[string]any{local, next, base} {
		for k := range m {
			keys[k] = true
		}
	}
	for k := range keys {
		v, cs := c.merge(get(base, k), get(local, k), get(next, k), append(slices.Clone(path), Step{Field: k}))
		if !IsMissing(v) {
			value[k] = v
		}
		conflicts = append(conflicts, cs...)
	}
	sortConflicts(conflicts)
	return value, conflicts
}

func (c *comparer) mergeLists(base, local, next keyedList, path Path) (any, []ValueConflict) {
	// Local order, then what only next added.
	order := slices.Clone(local.order)
	for _, k := range next.order {
		if _, ok := local.byKey[k]; !ok {
			order = append(order, k)
		}
	}
	value := []any{}
	var conflicts []ValueConflict
	for _, key := range order {
		at := append(slices.Clone(path), Step{Element: key})
		be, le, ne := base.get(key), local.get(key), next.get(key)
		var v any
		var cs []ValueConflict
		if isGroup(be) || isGroup(le) || isGroup(ne) {
			v, cs = c.whole(be, le, ne, at)
		} else {
			v, cs = c.merge(be, le, ne, at)
		}
		if group, ok := v.([]any); ok {
			value = append(value, group...)
		} else if !IsMissing(v) {
			value = append(value, v)
		}
		conflicts = append(conflicts, cs...)
	}
	return value, conflicts
}

func isGroup(v any) bool {
	_, ok := v.([]any)
	return ok
}

// whole merges a value as a whole: either side's change, or a conflict.
func (c *comparer) whole(base, local, next any, path Path) (any, []ValueConflict) {
	if c.sameElements(path, local, base) {
		return next, nil
	}
	if c.sameElements(path, next, base) || c.sameElements(path, next, local) {
		return local, nil
	}
	return local, []ValueConflict{{Path: path, Mine: local, Theirs: next}}
}

func sortConflicts(cs []ValueConflict) {
	slices.SortFunc(cs, func(a, b ValueConflict) int { return strings.Compare(a.Path.String(), b.Path.String()) })
}

// GetAt is the value at path, or Missing; a group for elements sharing a key.
func (s Schema) GetAt(root any, path Path) any {
	v := root
	for i, step := range path {
		if step.Element == "" && step.Field != "" {
			m, ok := object(v)
			if !ok {
				return Missing
			}
			v = get(m, step.Field)
			continue
		}
		k, ok := s.keyed(v, path[:i].Pattern())
		if !ok {
			return Missing
		}
		v = k.get(step.Element)
	}
	return v
}

// SetAt returns root with the value at path replaced, or removed when value
// is Missing; missing objects on the way are created, and a missing list
// element is appended.
func (s Schema) SetAt(root any, path Path, value any) any { return s.setAt(root, "", path, value) }

// setAt is SetAt for root at pattern p.
func (s Schema) setAt(root any, p string, path Path, value any) any {
	if len(path) == 0 {
		return value
	}
	step, rest := path[0], path[1:]
	if step.Element == "" && step.Field != "" {
		obj := map[string]any{}
		if m, ok := object(root); ok {
			maps.Copy(obj, m)
		}
		if v := s.setAt(get(obj, step.Field), field(p, step.Field), rest, value); IsMissing(v) {
			delete(obj, step.Field)
		} else {
			obj[step.Field] = v
		}
		return obj
	}
	return s.setElement(root, p, step.Element, rest, value)
}

// setElement replaces the elements with key, of root, the list at pattern p,
// where the first of them was.
func (s Schema) setElement(root any, p, key string, rest Path, value any) any {
	list, _ := root.([]any)
	var mine, others []any
	at := -1
	for _, el := range list {
		if k, ok := s.elementKey(el, p); ok && k == key {
			if at < 0 {
				at = len(others)
			}
			mine = append(mine, el)
			continue
		}
		others = append(others, el)
	}
	current := Missing
	switch len(mine) {
	case 0:
	case 1:
		current = mine[0]
	default:
		current = mine
	}
	v := s.setAt(current, p+"[]", rest, value)
	var replacement []any
	switch x := v.(type) {
	case missing:
	case []any:
		replacement = x
	default:
		replacement = []any{v}
	}
	if at < 0 {
		at = len(others)
	}
	out := make([]any, 0, len(others)+len(replacement))
	out = append(out, others[:at]...)
	out = append(out, replacement...)
	return append(out, others[at:]...)
}

// addressable is the part of path that still names one value in each of
// items: a path through a list element that has since become a group (a
// second grant on one entity, say) stops at the group, which the merge
// treats as a whole.
func (s Schema) addressable(path Path, items ...any) Path {
	for i := 0; i < len(path)-1; i++ {
		if path[i].Element == "" && path[i].Field != "" {
			continue
		}
		for _, item := range items {
			if isGroup(s.GetAt(item, path[:i+1])) {
				return path[:i+1]
			}
		}
	}
	return path
}

// Change is a value that differs: its path and its value after (Missing:
// removed).
type Change struct {
	Path  Path
	Value any
}

// Diff lists the values that differ from a to b, at the depth the merge
// compares them (objects by key, keyed lists by element, a group of
// elements sharing a key as a whole), in a stable order.
func (s Schema) Diff(a, b any) []Change {
	var out []Change
	s.comparer().diff(a, b, nil, &out)
	return out
}

func (c *comparer) diff(a, b any, path Path, out *[]Change) {
	if c.sameAt(path, a, b) {
		return
	}
	s := c.s
	ao, okA := object(a)
	bo, okB := object(b)
	if okA && okB {
		keys := map[string]bool{}
		for _, m := range []map[string]any{ao, bo} {
			for k := range m {
				keys[k] = true
			}
		}
		for _, k := range slices.Sorted(maps.Keys(keys)) {
			c.diff(get(ao, k), get(bo, k), append(slices.Clone(path), Step{Field: k}), out)
		}
		return
	}
	ka, okA := s.keyed(a, path.Pattern())
	kb, okB := s.keyed(b, path.Pattern())
	if okA && okB {
		order := slices.Clone(ka.order)
		for _, k := range kb.order {
			if !slices.Contains(order, k) {
				order = append(order, k)
			}
		}
		for _, k := range order {
			at := append(slices.Clone(path), Step{Element: k})
			ae, be := ka.get(k), kb.get(k)
			if isGroup(ae) || isGroup(be) {
				if !c.sameElements(at, ae, be) {
					*out = append(*out, Change{Path: at, Value: be})
				}
				continue
			}
			c.diff(ae, be, at, out)
		}
		return
	}
	*out = append(*out, Change{Path: path, Value: b})
}
