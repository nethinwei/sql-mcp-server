package workspace

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// schema is like the configuration's: fields and relationships keyed by name,
// grants by the entity they name, a top-level list (as the edge cases merge)
// by name; optional text unset when empty.
var schema = Schema{
	Lists: map[string]string{"fields[]": "name", "relationships[]": "name", "grants[]": "entity", "[]": "name"},
	Unset: map[string]bool{
		"source": true, "datasource": true, "schema": true, "kind": true, "description": true,
		"fields[].alias": true, "fields[].description": true, "fields[].mask": true,
	},
}

// js decodes a JSON literal; "" is Missing.
func js(t testing.TB, s string) any {
	t.Helper()
	if s == "" {
		return Missing
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func show(v any) string {
	if IsMissing(v) {
		return "<missing>"
	}
	data, _ := json.Marshal(v)
	return string(data)
}

func TestMergeTakesEachSideWhereOnlyItChanged(t *testing.T) {
	v, cs := schema.Merge(js(t, `{"name":"r","description":"a","grants":[]}`),
		js(t, `{"name":"r","description":"mine","grants":[]}`),
		js(t, `{"name":"r","description":"a","grants":[{"entity":"x","actions":["READ"]}]}`), nil)
	want := `{"name":"r","description":"mine","grants":[{"entity":"x","actions":["READ"]}]}`
	if len(cs) != 0 || !schema.Same(v, js(t, want)) {
		t.Fatalf("merged = %s %v", show(v), cs)
	}
}

func TestMergeReportsAValueBothChangedAndKeepsMine(t *testing.T) {
	v, cs := schema.Merge(js(t, `{"cost":{"maxRows":10,"softScore":1}}`), js(t, `{"cost":{"maxRows":20,"softScore":1}}`),
		js(t, `{"cost":{"maxRows":30,"softScore":5}}`), nil)
	if !schema.Same(v, js(t, `{"cost":{"maxRows":20,"softScore":5}}`)) || len(cs) != 1 ||
		cs[0].Path.String() != "cost.maxRows" || cs[0].Mine != 20.0 || cs[0].Theirs != 30.0 {
		t.Fatalf("merged = %s %+v", show(v), cs)
	}
}

func TestMergeGroupsGrantsOnOneEntity(t *testing.T) {
	g := func(entity string, actions ...string) string {
		data, _ := json.Marshal(map[string]any{"entity": entity, "actions": actions})
		return string(data)
	}
	list := func(gs ...string) string { return `{"grants":[` + strings.Join(gs, ",") + "]}" }
	base := list(g("a", "READ"), g("a", "AGGREGATE"))
	v, cs := schema.Merge(js(t, base), js(t, list(g("a", "READ", "CREATE"), g("a", "AGGREGATE"))),
		js(t, list(g("a", "READ"), g("a", "AGGREGATE"), g("b", "READ"))), nil)
	want := list(g("a", "READ", "CREATE"), g("a", "AGGREGATE"), g("b", "READ"))
	if len(cs) != 0 || !schema.Same(v, js(t, want)) {
		t.Fatalf("merged = %s %v", show(v), cs)
	}
	// Removing one grant of the group on one side and changing another on
	// the other is a conflict on the group, not a change to the wrong grant.
	_, cs = schema.Merge(js(t, base), js(t, list(g("a", "READ"), g("a", "DELETE"))),
		js(t, list(g("a", "AGGREGATE"))), nil)
	if len(cs) != 1 || cs[0].Path.String() != "grants[a]" {
		t.Fatalf("conflicts = %+v", cs)
	}
}

// A rule applies at its path only: a list elsewhere whose elements happen to
// have a name merges whole, and empty text elsewhere is a value.
func TestRulesApplyAtTheirPathOnly(t *testing.T) {
	_, cs := schema.Merge(js(t, `{"settings":{"hosts":[{"name":"a","port":1}]}}`),
		js(t, `{"settings":{"hosts":[{"name":"a","port":2}]}}`),
		js(t, `{"settings":{"hosts":[{"name":"a","port":1},{"name":"b","port":1}]}}`), nil)
	if len(cs) != 1 || cs[0].Path.String() != "settings.hosts" {
		t.Fatalf("an undeclared list must merge whole: %+v", cs)
	}
	if schema.Same(js(t, `{"settings":{"description":""}}`), js(t, `{"settings":{}}`)) {
		t.Fatal("empty text where no rule says so is a value")
	}
	if !schema.Same(js(t, `{"fields":[{"name":"id","mask":""}]}`), js(t, `{"fields":[{"name":"id"}]}`)) {
		t.Fatal("empty text where a rule says so is unset")
	}
}

func TestMergeRemovalAgainstChange(t *testing.T) {
	if _, cs := schema.Merge(js(t, `{"name":"r","d":1}`), Missing, js(t, `{"name":"r","d":2}`), nil); len(cs) != 1 ||
		len(cs[0].Path) != 0 || !IsMissing(cs[0].Mine) {
		t.Fatalf("conflicts = %+v", cs)
	}
	if v, cs := schema.Merge(js(t, `{"name":"r"}`), Missing, js(t, `{"name":"r"}`), nil); !IsMissing(v) || len(cs) != 0 {
		t.Fatalf("merged = %s %v", show(v), cs)
	}
}

func TestSameIgnoresKeyOrderAndUnsetOptionalText(t *testing.T) {
	if !schema.Same(js(t, `{"a":1,"description":""}`), js(t, `{"a":1}`)) ||
		!schema.Same(js(t, `{"a":1,"b":2}`), js(t, `{"b":2,"a":1}`)) {
		t.Fatal("same")
	}
}

func TestPaths(t *testing.T) {
	role := js(t, `{"fields":[{"name":"id","mask":"x"}]}`)
	path := Path{{Field: "fields"}, {Element: "id"}, {Field: "mask"}}
	if schema.GetAt(role, path) != "x" || path.String() != "fields[id].mask" {
		t.Fatalf("get = %v %s", schema.GetAt(role, path), path)
	}
	for _, c := range []struct {
		path        Path
		value, want string
	}{
		{path, `"y"`, `{"fields":[{"name":"id","mask":"y"}]}`},
		{path, ``, `{"fields":[{"name":"id"}]}`},
		{Path{{Field: "fields"}, {Element: "email"}}, `{"name":"email"}`,
			`{"fields":[{"name":"id","mask":"x"},{"name":"email"}]}`},
	} {
		if got := schema.SetAt(role, c.path, js(t, c.value)); !schema.Same(got, js(t, c.want)) {
			t.Errorf("set %s %s = %s", c.path, c.value, show(got))
		}
	}
}

func TestMergeEdgeCases(t *testing.T) {
	for _, c := range []struct {
		name                    string
		base, local, next, want string
		conflicts               int
	}{
		{"both add the same element", `[]`, `[{"name":"x"}]`, `[{"name":"x"}]`, `[{"name":"x"}]`, 0},
		{"both add an element with different values", `[]`, `[{"name":"x","a":1}]`, `[{"name":"x","a":2}]`,
			`[{"name":"x","a":1}]`, 1},
		{"one removes, the other keeps", `[{"name":"x"}]`, `[]`, `[{"name":"x"}]`, `[]`, 0},
		{"one removes, the other changes", `[{"name":"x","a":1}]`, `[]`, `[{"name":"x","a":2}]`, `[]`, 1},
		{"both remove", `[{"name":"x"}]`, `[]`, `[]`, `[]`, 0},
		{"elements sharing a name merge as one group", `[{"name":"x"}]`, `[{"name":"x"},{"name":"x"}]`,
			`[{"name":"y"}]`, `[{"name":"x"},{"name":"x"},{"name":"y"}]`, 1},
		{"a list of values is one value", `["a"]`, `["a","b"]`, `["a","c"]`, `["a","b"]`, 1},
		{"a second grant on one entity joins its group", `[{"entity":"a","actions":["READ"]}]`,
			`[{"entity":"a","actions":["READ"]},{"entity":"a","actions":["CREATE"]}]`,
			`[{"entity":"a","actions":["READ","AGGREGATE"]}]`,
			`[{"entity":"a","actions":["READ"]},{"entity":"a","actions":["CREATE"]}]`, 1},
		{"a group one side kept and the other changed takes the change",
			`[{"entity":"a","actions":["READ"]},{"entity":"a","actions":["CREATE"]}]`,
			`[{"entity":"a","actions":["READ"]},{"entity":"a","actions":["CREATE"]}]`,
			`[{"entity":"a","actions":["READ"]}]`, `[{"entity":"a","actions":["READ"]}]`, 0},
		{"local order wins, new elements are appended", `[{"name":"a"},{"name":"b"}]`, `[{"name":"b"},{"name":"a"}]`,
			`[{"name":"a"},{"name":"b"},{"name":"c"}]`, `[{"name":"b"},{"name":"a"},{"name":"c"}]`, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			v, cs := schema.Merge(js(t, c.base), js(t, c.local), js(t, c.next), nil)
			if show(v) != show(js(t, c.want)) || len(cs) != c.conflicts {
				t.Fatalf("merged = %s, %d conflicts, want %s, %d", show(v), len(cs), c.want, c.conflicts)
			}
		})
	}
}

func Example_pathString() {
	fmt.Println(Path{{Field: "grants"}, {Element: "orders"}, {Field: "actions"}})
	// Output: grants[orders].actions
}

func TestDiff(t *testing.T) {
	a := js(t, `{"name":"analyst","description":"a","grants":[{"entity":"x","actions":["READ"]}]}`)
	b := js(t, `{"name":"analyst","description":"b",
		"grants":[{"entity":"x","actions":["READ"]},{"entity":"y","actions":["READ"]}]}`)
	var got []string
	for _, c := range schema.Diff(a, b) {
		got = append(got, c.Path.String()+"="+show(c.Value))
	}
	want := []string{`description="b"`, `grants[y]={"actions":["READ"],"entity":"y"}`}
	if strings.Join(got, ";") != strings.Join(want, ";") {
		t.Fatalf("diff = %v", got)
	}
	if len(schema.Diff(js(t, `{"a":1,"description":""}`), js(t, `{"a":1}`))) != 0 {
		t.Fatal("unset optional text differs")
	}
}
