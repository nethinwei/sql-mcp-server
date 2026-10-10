package graph

import (
	"testing"

	"github.com/99designs/gqlgen/client"
)

// Renaming an entity in the workspace (the new item, renamedFrom the old
// key) moves the references to it on the server, keeps the table it reads,
// and commits.
func TestEditWorkspaceRenamesAnEntity(t *testing.T) {
	_, c := planningHarness(t)
	var read struct{ Workspace workspaceState }
	mustPost(t, c, readWorkspace, &read)
	customers := read.Workspace.Sections["entities"].([]any)[0].(map[string]any)
	clients := map[string]any{}
	for k, v := range customers {
		clients[k] = v
	}
	clients["name"] = "clients"
	var edited struct{ EditWorkspace workspaceState }
	mustPost(t, c, `mutation($in: WorkspaceEditInput!) { editWorkspace(input: $in) { version sections } }`, &edited,
		client.Var("in", map[string]any{"items": []any{map[string]any{
			"kind": "entity", "key": "shop.clients", "value": clients, "renamedFrom": "shop.customers",
		}}}))
	entities := edited.EditWorkspace.Sections["entities"].([]any)
	grants := edited.EditWorkspace.role("analyst")["grants"].([]any)
	if len(entities) != 1 || entities[0].(map[string]any)["source"] != "customers" ||
		grants[0].(map[string]any)["entity"] != "clients" {
		t.Fatalf("entities = %v, analyst grants = %v", entities, grants)
	}
	var committed struct{ CommitWorkspace struct{ State string } }
	mustPost(t, c, `mutation($v: Int!) { commitWorkspace(input: {version: $v, expectedPublished: "1"}) { state } }`,
		&committed, client.Var("v", edited.EditWorkspace.Version))
	if committed.CommitWorkspace.State != "PUBLISHED" {
		t.Fatalf("committed = %+v", committed)
	}
}
