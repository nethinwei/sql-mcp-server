package graph

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/99designs/gqlgen/client"

	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
)

// A table the serving configuration found missing is pending, and a plan's
// sync removes its entity.
func TestGoneTablesPendingAndRemoved(t *testing.T) {
	h, c := planningHarness(t)
	h.resolver.App.Missing = func() bootstrap.Missing {
		return bootstrap.Missing{Tables: map[string]bool{"shop.customers": true}}
	}
	var pending struct {
		Pending struct {
			GoneTables []struct {
				Key, Ref, Entity string
				Scanned          bool
			}
		}
	}
	mustPost(t, c, `{ pending { goneTables { key ref entity scanned } } }`, &pending)
	gone := pending.Pending.GoneTables
	if len(gone) != 1 || gone[0].Entity != "shop.customers" || gone[0].Scanned || gone[0].Key != "gone:shop.customers" {
		t.Fatalf("gone = %+v", gone)
	}
	var preview struct {
		PreviewPlan struct {
			Report struct {
				Changes []struct {
					Kind     string
					Entities []string
				}
			}
			Removed    []string
			Validation struct{ Ok bool }
		}
	}
	mustPost(t, c, `query($p: JSON!) { previewPlan(input: {plan: $p}) {
		report { changes { kind entities } } removed validation { ok } } }`, &preview,
		client.Var("p", map[string]any{"sync": map[string]any{"removeTables": true}}))
	ch := preview.PreviewPlan.Report.Changes
	if len(ch) != 1 || ch[0].Kind != "removeTable" || len(ch[0].Entities) != 1 || !preview.PreviewPlan.Validation.Ok ||
		!slices.Equal(preview.PreviewPlan.Removed, []string{"shop.customers"}) {
		t.Fatalf("preview = %+v", preview.PreviewPlan)
	}
}

// The console's entity sync: a plan scoped to entities syncs them alone and
// applies to the workspace.
func TestSyncEntitiesByScope(t *testing.T) {
	_, c := planningHarness(t)
	plan := map[string]any{"scope": map[string]any{"entities": []string{"shop.customers"}},
		"sync": map[string]any{"remove": true, "keys": true, "removeTables": true}}
	var preview struct {
		PreviewPlan struct {
			PlanID string
			Report struct {
				Changes []struct {
					Kind     string
					Entities []string
				}
			}
			Removed []string
		}
	}
	mustPost(t, c, `query($p: JSON!) { previewPlan(input: {plan: $p}) { planId
		report { changes { kind entities } } removed } }`, &preview, client.Var("p", plan))
	var kinds []string
	for _, ch := range preview.PreviewPlan.Report.Changes {
		if !slices.Equal(ch.Entities, []string{"shop.customers"}) {
			t.Fatalf("a change outside the scope: %+v", ch)
		}
		kinds = append(kinds, ch.Kind)
	}
	// The table has a primary key the entity does not declare, and lost a column.
	if !slices.Equal(kinds, []string{"syncKeys", "syncRemove"}) || len(preview.PreviewPlan.Removed) != 0 {
		t.Fatalf("preview = %+v", preview.PreviewPlan)
	}
	var applied struct {
		ApplyPlan struct{ Items []struct{ Kind, Key string } }
	}
	mustPost(t, c, `mutation($p: JSON!, $id: String!) { applyPlan(input: {plan: $p, planId: $id}) {
		items { kind key } } }`, &applied, client.Var("p", plan), client.Var("id", preview.PreviewPlan.PlanID))
	var read struct{ Workspace workspaceState }
	mustPost(t, c, readWorkspace, &read)
	entities, _ := json.Marshal(read.Workspace.Sections["entities"])
	if len(applied.ApplyPlan.Items) == 0 || strings.Contains(string(entities), "legacy_col") {
		t.Fatalf("applied = %+v, entities = %s", applied, entities)
	}
}
