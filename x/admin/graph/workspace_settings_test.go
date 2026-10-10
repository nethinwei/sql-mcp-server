package graph

import (
	"strconv"
	"strings"
	"testing"

	"github.com/99designs/gqlgen/client"
)

// The settings item is the settings as they are: a key removed from it is
// removed from the configuration (its default applies), not kept from the
// base.
func TestRemovedSettingIsRemoved(t *testing.T) {
	h, c := planningHarness(t)
	commit := func(settings map[string]any, previous any) string {
		t.Helper()
		var edited struct{ EditWorkspace workspaceState }
		mustPost(t, c, editMutation, &edited, client.Var("in", map[string]any{"items": []any{
			map[string]any{"kind": "settings", "key": "settings", "value": settings, "previous": previous},
		}}))
		var read struct{ Workspace workspaceState }
		mustPost(t, c, readWorkspace, &read)
		var committed struct{ CommitWorkspace struct{ ID string } }
		mustPost(t, c, `mutation($v: Int!, $p: ID!) { commitWorkspace(input: {version: $v, expectedPublished: $p}) { id } }`,
			&committed, client.Var("v", edited.EditWorkspace.Version), client.Var("p", read.Workspace.Base))
		id, _ := strconv.ParseInt(committed.CommitWorkspace.ID, 10, 64)
		return h.payload(t, id)
	}
	var read struct{ Workspace workspaceState }
	mustPost(t, c, readWorkspace, &read)
	base := read.Workspace.Sections["settings"].(map[string]any)
	off := map[string]any{}
	for k, v := range base {
		off[k] = v
	}
	off["mask"] = map[string]any{"enabled": false}
	if payload := commit(off, base); !strings.Contains(payload, "enabled: false") {
		t.Fatalf("masking not turned off:\n%s", payload)
	}
	removed := map[string]any{}
	for k, v := range off {
		if k != "mask" {
			removed[k] = v
		}
	}
	if payload := commit(removed, off); strings.Contains(payload, "mask:\n  enabled: false") {
		t.Fatalf("the removed mask setting was kept from the base:\n%s", payload)
	}
}
