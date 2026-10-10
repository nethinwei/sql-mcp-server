package workspace

import (
	"encoding/json"
	"maps"
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

// Schema holds the rules the merge needs of the values it merges.
type Schema struct {
	// ListKeys are the properties that key the elements of a list, tried in
	// order (e.g. name, then entity); a list with an element keyed by none
	// of them is one value.
	ListKeys []string
	// OptionalText are the properties whose empty text means unset.
	OptionalText []string
}

// Step is a step into a value: an object property, or the element of a list
// with a key.
type Step struct {
	Field   string `json:"field,omitempty"`
	Element string `json:"element,omitempty"`
}

// Path is a sequence of steps; empty is the whole value.
type Path []Step

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

// normalize drops unset optional text (empty or null) and Missing values.
func (s Schema) normalize(v any, key string) any {
	if slices.Contains(s.OptionalText, key) && (v == "" || v == nil) {
		return Missing
	}
	switch x := v.(type) {
	case []any:
		out := make([]any, len(x))
		for i, el := range x {
			out[i] = s.normalize(el, "")
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, el := range x {
			if n := s.normalize(el, k); !IsMissing(n) {
				out[k] = n
			}
		}
		return out
	}
	return v
}

// Canonical is v's canonical JSON: keys sorted, unset optional text dropped.
func (s Schema) Canonical(v any) string {
	n := s.normalize(v, "")
	if IsMissing(n) {
		n = nil
	}
	data, _ := json.Marshal(n)
	return string(data)
}

// Same reports whether two values mean the same, regardless of key order or
// unset optional text.
func (s Schema) Same(a, b any) bool { return s.Canonical(a) == s.Canonical(b) }

// sameAt compares two values in place at path: an optional text property
// unset equals it empty.
func (s Schema) sameAt(path Path, a, b any) bool {
	if len(path) > 0 && path[len(path)-1].Field != "" {
		last := path[len(path)-1].Field
		return s.Same(map[string]any{last: a}, map[string]any{last: b})
	}
	return s.Same(a, b)
}

func (s Schema) elementKey(el any) (string, bool) {
	m, ok := object(el)
	if !ok {
		return "", false
	}
	for _, k := range s.ListKeys {
		if key, ok := m[k].(string); ok {
			return key, true
		}
	}
	return "", false
}

// keyedList is a list of keyed elements, in order of first appearance;
// elements sharing a key map to the list of them (a group).
type keyedList struct {
	order []string
	byKey map[string]any
}

func (s Schema) keyed(v any) (keyedList, bool) {
	list, ok := v.([]any)
	if !ok {
		return keyedList{}, false
	}
	k := keyedList{byKey: map[string]any{}}
	groups := map[string][]any{}
	for _, el := range list {
		key, ok := s.elementKey(el)
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

// Merge merges base → local and base → next.
func (s Schema) Merge(base, local, next any, path Path) (any, []ValueConflict) {
	if s.sameAt(path, local, base) {
		return next, nil
	}
	if s.sameAt(path, next, base) || s.sameAt(path, next, local) {
		return local, nil
	}
	bo, okB := object(base)
	lo, okL := object(local)
	no, okN := object(next)
	if okB && okL && okN {
		return s.mergeObjects(bo, lo, no, path)
	}
	if IsMissing(base) {
		base = []any{}
	}
	b, okB := s.keyed(base)
	l, okL := s.keyed(local)
	n, okN := s.keyed(next)
	if okB && okL && okN {
		return s.mergeLists(b, l, n, path)
	}
	return local, []ValueConflict{{Path: path, Mine: local, Theirs: next}}
}

func (s Schema) mergeObjects(base, local, next map[string]any, path Path) (any, []ValueConflict) {
	value := map[string]any{}
	var conflicts []ValueConflict
	keys := map[string]bool{}
	for _, m := range []map[string]any{local, next, base} {
		for k := range m {
			keys[k] = true
		}
	}
	for k := range keys {
		v, c := s.Merge(get(base, k), get(local, k), get(next, k), append(slices.Clone(path), Step{Field: k}))
		if !IsMissing(v) {
			value[k] = v
		}
		conflicts = append(conflicts, c...)
	}
	sortConflicts(conflicts)
	return value, conflicts
}

func (s Schema) mergeLists(base, local, next keyedList, path Path) (any, []ValueConflict) {
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
		var c []ValueConflict
		if isGroup(be) || isGroup(le) || isGroup(ne) {
			v, c = s.whole(be, le, ne, at)
		} else {
			v, c = s.Merge(be, le, ne, at)
		}
		if group, ok := v.([]any); ok {
			value = append(value, group...)
		} else if !IsMissing(v) {
			value = append(value, v)
		}
		conflicts = append(conflicts, c...)
	}
	return value, conflicts
}

func isGroup(v any) bool {
	_, ok := v.([]any)
	return ok
}

// whole merges a value as a whole: either side's change, or a conflict.
func (s Schema) whole(base, local, next any, path Path) (any, []ValueConflict) {
	if s.Same(local, base) {
		return next, nil
	}
	if s.Same(next, base) || s.Same(next, local) {
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
	for _, step := range path {
		if step.Element == "" && step.Field != "" {
			m, ok := object(v)
			if !ok {
				return Missing
			}
			v = get(m, step.Field)
			continue
		}
		k, ok := s.keyed(v)
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
func (s Schema) SetAt(root any, path Path, value any) any {
	if len(path) == 0 {
		return value
	}
	step, rest := path[0], path[1:]
	if step.Element == "" && step.Field != "" {
		obj := map[string]any{}
		if m, ok := object(root); ok {
			maps.Copy(obj, m)
		}
		if v := s.SetAt(get(obj, step.Field), rest, value); IsMissing(v) {
			delete(obj, step.Field)
		} else {
			obj[step.Field] = v
		}
		return obj
	}
	return s.setElement(root, step.Element, rest, value)
}

// setElement replaces the elements with key where the first of them was.
func (s Schema) setElement(root any, key string, rest Path, value any) any {
	list, _ := root.([]any)
	var mine, others []any
	at := -1
	for _, el := range list {
		if k, ok := s.elementKey(el); ok && k == key {
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
	v := s.SetAt(current, rest, value)
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
	s.diff(a, b, nil, &out)
	return out
}

func (s Schema) diff(a, b any, path Path, out *[]Change) {
	if s.sameAt(path, a, b) {
		return
	}
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
			s.diff(get(ao, k), get(bo, k), append(slices.Clone(path), Step{Field: k}), out)
		}
		return
	}
	ka, okA := s.keyed(a)
	kb, okB := s.keyed(b)
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
				if !s.Same(ae, be) {
					*out = append(*out, Change{Path: at, Value: be})
				}
				continue
			}
			s.diff(ae, be, at, out)
		}
		return
	}
	*out = append(*out, Change{Path: path, Value: b})
}
