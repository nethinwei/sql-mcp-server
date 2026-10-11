package graph

import (
	"strconv"
	"strings"
	"testing"

	"github.com/99designs/gqlgen/client"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/x/configedit"
)

const rotated = "sha256:2222222222222222222222222222222222222222222222222222222222222222"

// A draft that revokes or rotates a user's token carries it into the
// workspace it is merged into, and committing the workspace publishes it.
func TestMergeDraftCarriesTokenChanges(t *testing.T) {
	for name, token := range map[string]string{"revoked": "", "rotated": rotated} {
		t.Run(name, func(t *testing.T) {
			h, c := planningHarness(t)
			drafted := h.draftOn(t, "1", configedit.Edit{Users: []configedit.User{
				{Name: "alice", UserSpec: config.UserSpec{Roles: config.List[string]{"analyst"}, TokenHash: &token}},
			}})
			var merged struct{ MergeDraft workspaceState }
			mustPost(t, c, `mutation($id: ID!) { mergeDraft(id: $id) { base version sections conflicts { id } } }`,
				&merged, client.Var("id", drafted))
			// A user without a token has no hash in the workspace.
			alice := merged.MergeDraft.Sections["users"].([]any)[0].(map[string]any)
			if hash, _ := alice["tokenHash"].(string); hash != token || len(merged.MergeDraft.Conflicts) != 0 {
				t.Fatalf("merged = %+v", merged.MergeDraft)
			}
			var committed struct{ CommitWorkspace struct{ ID string } }
			mustPost(t, c, `mutation($v: Int!) { commitWorkspace(input: {version: $v, expectedPublished: "1"}) { id } }`,
				&committed, client.Var("v", merged.MergeDraft.Version))
			id, _ := strconv.ParseInt(committed.CommitWorkspace.ID, 10, 64)
			payload := h.payload(t, id)
			want := token != "" && strings.Contains(payload, "tokenHash: "+token) ||
				token == "" && !strings.Contains(payload, "tokenHash")
			if !want {
				t.Fatalf("published:\n%s", payload)
			}
		})
	}
}

// Two writers rotating the same token differently conflict, as any value.
func TestConcurrentTokenRotationsConflict(t *testing.T) {
	_, c := planningHarness(t)
	var read struct{ Workspace workspaceState }
	mustPost(t, c, readWorkspace, &read)
	alice := read.Workspace.Sections["users"].([]any)[0].(map[string]any)
	write := func(token string) workspaceState {
		value := map[string]any{"name": "alice", "roles": []any{"analyst"}, "tokenHash": token}
		var edited struct{ EditWorkspace workspaceState }
		mustPost(t, c, editMutation, &edited, client.Var("in", map[string]any{"items": []any{
			map[string]any{"kind": "user", "key": "alice", "value": value, "previous": alice},
		}}))
		return edited.EditWorkspace
	}
	write(rotated)
	if got := write(""); len(got.Conflicts) != 1 || got.Conflicts[0].Path != "tokenHash" {
		t.Fatalf("conflicts = %+v", got.Conflicts)
	}
}

// A token revoked here and in a draft taken in is the same change: an empty
// hash is no token.
func TestRevokedTwiceDoesNotConflict(t *testing.T) {
	h, c := planningHarness(t)
	var read struct{ Workspace workspaceState }
	mustPost(t, c, readWorkspace, &read)
	alice := read.Workspace.Sections["users"].([]any)[0].(map[string]any)
	revoked := map[string]any{"name": "alice", "roles": []any{"analyst"}, "tokenHash": ""}
	var edited struct{ EditWorkspace workspaceState }
	mustPost(t, c, editMutation, &edited, client.Var("in", map[string]any{"items": []any{
		map[string]any{"kind": "user", "key": "alice", "value": revoked, "previous": alice},
	}}))
	drafted := h.draftOn(t, "1", configedit.Edit{Users: []configedit.User{
		{Name: "alice", UserSpec: config.UserSpec{Roles: config.List[string]{"analyst"}}},
	}})
	var merged struct{ MergeDraft workspaceState }
	mustPost(t, c, `mutation($id: ID!) { mergeDraft(id: $id) { base version sections conflicts { id path } } }`,
		&merged, client.Var("id", drafted))
	if len(merged.MergeDraft.Conflicts) != 0 {
		t.Fatalf("conflicts = %+v", merged.MergeDraft.Conflicts)
	}
}
