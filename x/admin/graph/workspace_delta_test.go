package graph

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/99designs/gqlgen/client"
)

type deltaItem struct {
	Kind, Key string
	Value     any
}

type itemsAnswer struct {
	Workspace struct {
		Version int
		Items   struct {
			Complete bool
			Items    []deltaItem
		}
	}
}

func readItems(t *testing.T, c *client.Client, since *int) itemsAnswer {
	t.Helper()
	var out itemsAnswer
	mustPost(t, c, `query($s: Int) { workspace { version items(since: $s) { complete items { kind key value } } } }`,
		&out, client.Var("s", since))
	return out
}

// applyDelta is items with the changed ones taken in: replaced where they
// are, appended when new, dropped when removed (a nil value).
func applyDelta(items, changed []deltaItem) []deltaItem {
	at := map[string]int{}
	out := append([]deltaItem(nil), items...)
	for i, it := range out {
		at[it.Kind+"\x00"+it.Key] = i
	}
	var gone []int
	for _, it := range changed {
		i, ok := at[it.Kind+"\x00"+it.Key]
		switch {
		case ok && it.Value == nil:
			gone = append(gone, i)
		case ok:
			out[i] = it
		case it.Value != nil:
			out = append(out, it)
		}
	}
	keep := out[:0]
	for i, it := range out {
		drop := false
		for _, g := range gone {
			drop = drop || g == i
		}
		if !drop {
			keep = append(keep, it)
		}
	}
	return keep
}

// byRef is items by kind and key, comparable whatever their order.
func byRef(items []deltaItem) map[string]string {
	out := map[string]string{}
	for _, it := range items {
		data, _ := json.Marshal(it.Value)
		out[it.Kind+"\x00"+it.Key] = string(data)
	}
	return out
}

// A console holding the workspace at some version takes in the items
// changed since (Workspace.items(since)) and then holds it as it is, after
// any sequence of the console's and agents' operations: edits that merge or
// conflict, renames that move references, conflicts resolved, plans applied
// and checkpoints; an operation moving it onto another revision answers it
// complete.
func TestWorkspaceItemsSinceAVersion(t *testing.T) {
	for seed := range uint64(4) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			checkDeltas(t, rand.New(rand.NewPCG(seed, 7)))
		})
	}
}

// deltaSession runs random operations on a workspace through the admin API.
type deltaSession struct {
	t   *testing.T
	c   *client.Client
	rnd *rand.Rand
}

func (d deltaSession) role(name, desc string) map[string]any {
	return map[string]any{"name": name, "description": desc,
		"grants": []any{map[string]any{"entity": "customers", "actions": []any{"READ"}}}}
}

// current is the value of an item now; nil when absent.
func (d deltaSession) current(kind, key string) any {
	for _, it := range readItems(d.t, d.c, nil).Workspace.Items.Items {
		if it.Kind == kind && it.Key == key {
			return it.Value
		}
	}
	return nil
}

func (d deltaSession) edit(items ...any) {
	mustPost(d.t, d.c, editMutation, &map[string]any{}, client.Var("in", map[string]any{"items": items}))
}

// writeRole writes a role from what is there or, stale, from another value:
// it merges, or conflicts.
func (d deltaSession) writeRole(stale bool) {
	name := fmt.Sprintf("r%d", d.rnd.IntN(3))
	previous := d.current("role", name)
	if stale {
		previous = d.role(name, "stale")
	}
	d.edit(map[string]any{"kind": "role", "key": name, "value": d.role(name, fmt.Sprint(d.rnd.IntN(9))),
		"previous": previous})
}

func (d deltaSession) renameRole() {
	from, to := fmt.Sprintf("r%d", d.rnd.IntN(3)), fmt.Sprintf("r%d", d.rnd.IntN(3))
	if was := d.current("role", from); was != nil && from != to {
		d.edit(map[string]any{"kind": "role", "key": to, "value": d.role(to, "renamed"), "renamedFrom": from,
			"previous": d.current("role", to)},
			map[string]any{"kind": "role", "key": from, "value": nil, "previous": was})
	}
}

// renameEntity renames the entity back and forth: grants on it follow.
func (d deltaSession) renameEntity() {
	from, to, name := "shop.customers", "shop.clients", "clients"
	if d.current("entity", from) == nil {
		from, to, name = to, from, "customers"
	}
	v := d.current("entity", from).(map[string]any)
	d.edit(map[string]any{"kind": "entity", "key": to, "renamedFrom": from,
		"value": map[string]any{"name": name, "source": "customers", "datasource": "shop", "fields": v["fields"]}},
		map[string]any{"kind": "entity", "key": from, "value": nil, "previous": v})
}

func (d deltaSession) removeUser() {
	if was := d.current("user", "alice"); was != nil {
		d.edit(map[string]any{"kind": "user", "key": "alice", "value": nil, "previous": was})
	}
}

func (d deltaSession) resolve() {
	var w struct {
		Workspace struct{ Conflicts []struct{ ID string } }
	}
	mustPost(d.t, d.c, `{ workspace { conflicts { id } } }`, &w)
	if len(w.Workspace.Conflicts) > 0 {
		mustPost(d.t, d.c, `mutation($id: String!, $t: Boolean!) { resolveConflict(id: $id, theirs: $t) { version } }`,
			&map[string]any{}, client.Var("id", w.Workspace.Conflicts[0].ID), client.Var("t", d.rnd.IntN(2) == 0))
	}
}

// checkpoint saves a draft; refused while conflicts are open or the
// workspace does not hold together.
func (d deltaSession) checkpoint() {
	_ = d.c.Post(`mutation { checkpointWorkspace(comment: "c") { id } }`, &map[string]any{})
}

func (d deltaSession) applyPlan() {
	var preview struct{ PreviewPlan struct{ PlanID string } }
	if d.c.Post(`query($p: JSON!) { previewPlan(input: {plan: $p}) { planId } }`, &preview,
		client.Var("p", mustJSON(importPlan))) != nil {
		return
	}
	_ = d.c.Post(`mutation($p: JSON!, $id: String!) { applyPlan(input: {plan: $p, planId: $id}) {
		items { key } } }`, &map[string]any{}, client.Var("p", mustJSON(importPlan)),
		client.Var("id", preview.PreviewPlan.PlanID))
}

// check takes in, from every version held, the items changed since.
func (d deltaSession) check(held map[int][]deltaItem) int {
	full := readItems(d.t, d.c, nil).Workspace
	if !full.Items.Complete {
		d.t.Fatal("items without since must be complete")
	}
	held[full.Version] = full.Items.Items
	for since, items := range held {
		got := readItems(d.t, d.c, &since).Workspace
		if got.Items.Complete {
			d.t.Fatalf("since %d (now %d): answered complete, though no operation moved the workspace",
				since, full.Version)
		}
		if after := applyDelta(items, got.Items.Items); !reflect.DeepEqual(byRef(after), byRef(full.Items.Items)) {
			d.t.Fatalf("since %d (now %d): items taken in differ:\n got  %v\n want %v", since, full.Version,
				byRef(after), byRef(full.Items.Items))
		}
	}
	return full.Version
}

func checkDeltas(t *testing.T, rnd *rand.Rand) {
	_, c := planningHarness(t)
	d := deltaSession{t: t, c: c, rnd: rnd}
	held := map[int][]deltaItem{0: readItems(t, c, nil).Workspace.Items.Items}
	steps := []func(){
		func() { d.writeRole(false) }, func() { d.writeRole(true) }, d.renameRole, d.renameEntity, d.removeUser,
		d.resolve, d.checkpoint, d.applyPlan,
	}
	version := 0
	for range 60 {
		steps[rnd.IntN(len(steps))]()
		version = d.check(held)
	}
	if version < 15 {
		t.Fatalf("only %d writes: the sequence checked little", version)
	}
	// Moving onto another revision answers complete.
	mustPost(t, c, `mutation { resetWorkspace(to: "1") { version } }`, &map[string]any{})
	if !readItems(t, c, &version).Workspace.Items.Complete {
		t.Fatal("a reset must answer complete")
	}
}
