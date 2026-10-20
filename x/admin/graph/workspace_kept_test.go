package graph

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/99designs/gqlgen/client"

	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/admin/configapp"
	"github.com/nethinwei/sql-mcp-server/x/configstore"
)

// The configuration store keeps the workspace it last read or wrote (see
// configstore.SQLStore.Workspace): after the operations of the console (a
// plan applied, an entity renamed, which moves the references to it, a
// checkpoint), the one kept is the one stored, as another process reads it.
func TestKeptWorkspaceIsTheStoredOne(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	spec := configstore.Spec{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "plans.db")}
	if err := configstore.Init(ctx, spec, nil); err != nil {
		t.Fatal(err)
	}
	open := func() *configstore.SQLStore {
		store, err := configstore.Open(ctx, spec, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return store
	}
	kept := open()
	h.resolver.App.Plans = kept
	c := h.client(auth.PermAll)
	scanSchema(t, c, `datasource: "shop", schemas: ["public"]`)
	var preview struct{ PreviewPlan struct{ PlanID string } }
	mustPost(t, c, `query($p: JSON!) { previewPlan(input: {plan: $p}) { planId } }`, &preview,
		client.Var("p", mustJSON(importPlan)))
	mustPost(t, c, `mutation($p: JSON!, $id: String!) { applyPlan(input: {plan: $p, planId: $id}) {
		items { kind key } } }`, &map[string]any{}, client.Var("p", mustJSON(importPlan)),
		client.Var("id", preview.PreviewPlan.PlanID))
	var read struct{ Workspace workspaceState }
	mustPost(t, c, readWorkspace, &read)
	customers := read.Workspace.Sections["entities"].([]any)[0].(map[string]any)
	clients := map[string]any{"name": "clients", "source": "customers", "datasource": "shop",
		"fields": customers["fields"]}
	mustPost(t, c, editMutation, &map[string]any{}, client.Var("in", map[string]any{"items": []any{
		map[string]any{"kind": "entity", "key": "shop.clients", "value": clients, "renamedFrom": "shop.customers"},
		map[string]any{"kind": "entity", "key": "shop.customers", "value": nil, "previous": customers}}}))
	h.checkpoint(t, c)
	got, _, err := kept.Workspace(ctx, "root")
	if err != nil {
		t.Fatal(err)
	}
	want, _, err := open().Workspace(ctx, "root")
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := json.Marshal(want.Items); !strings.Contains(string(data), `"entity":"clients"`) {
		t.Fatalf("the rename did not move the grant on the entity: %s", data)
	}
	got.UpdatedAt, want.UpdatedAt = got.UpdatedAt.Truncate(time.Microsecond), want.UpdatedAt.Truncate(time.Microsecond)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("kept workspace differs from the stored one:\nkept   %+v\nstored %+v", got, want)
	}
}

// The caches share what they keep (revisions' items, the last workspace):
// a conflict resolved with the other side's value, and a reset, write
// nothing into them. The workspace kept is the one stored, and a revision's
// sections are its payload's.
func TestCachesKeepWhatTheyRead(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	spec := configstore.Spec{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "plans.db")}
	if err := configstore.Init(ctx, spec, nil); err != nil {
		t.Fatal(err)
	}
	kept, err := configstore.Open(ctx, spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = kept.Close() })
	h.resolver.App.Plans = kept
	c := h.client(auth.PermAll)
	var edited struct{ EditWorkspace workspaceState }
	support := map[string]any{"name": "support", "description": "x"}
	mustPost(t, c, editMutation, &edited, client.Var("in", roleWrite(support, nil)))
	stale := map[string]any{"name": "support", "description": "y"}
	mustPost(t, c, editMutation, &edited, client.Var("in", roleWrite(map[string]any{"name": "support",
		"description": "z"}, stale)))
	if len(edited.EditWorkspace.Conflicts) != 1 {
		t.Fatalf("conflicts = %+v", edited.EditWorkspace.Conflicts)
	}
	mustPost(t, c, `mutation($id: String!) { resolveConflict(id: $id, theirs: true) { version } }`,
		&map[string]any{}, client.Var("id", edited.EditWorkspace.Conflicts[0].ID))
	assertKeptIsStored(t, kept, spec)
	mustPost(t, c, `mutation { resetWorkspace(to: "1") { version } }`, &map[string]any{})
	assertKeptIsStored(t, kept, spec)
	var rev struct{ Revision struct{ Sections any } }
	mustPost(t, c, `{ revision(id: "1") { sections } }`, &rev)
	sections, err := configapp.SectionsOfPayload([]byte(h.payload(t, 1)))
	if err != nil {
		t.Fatal(err)
	}
	var want any
	data, _ := json.Marshal(sections)
	_ = json.Unmarshal(data, &want)
	if !reflect.DeepEqual(rev.Revision.Sections, want) {
		t.Errorf("revision 1 sections = %v\nits payload's = %v", rev.Revision.Sections, want)
	}
}

func assertKeptIsStored(t *testing.T, kept *configstore.SQLStore, spec configstore.Spec) {
	t.Helper()
	ctx := context.Background()
	fresh, err := configstore.Open(ctx, spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fresh.Close() }()
	got, _, err := kept.Workspace(ctx, "root")
	if err != nil {
		t.Fatal(err)
	}
	want, _, err := fresh.Workspace(ctx, "root")
	if err != nil {
		t.Fatal(err)
	}
	got.UpdatedAt, want.UpdatedAt = got.UpdatedAt.Truncate(time.Microsecond), want.UpdatedAt.Truncate(time.Microsecond)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("kept workspace differs from the stored one:\nkept   %+v\nstored %+v", got, want)
	}
}
