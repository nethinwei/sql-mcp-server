package graph

import (
	"testing"

	"github.com/99designs/gqlgen/client"
)

// A table the serving configuration found missing is pending, and a plan's
// sync removes its entity.
func TestGoneTablesPendingAndRemoved(t *testing.T) {
	h, c := planningHarness(t)
	h.resolver.App.MissingTables = func() map[string]bool { return map[string]bool{"shop.customers": true} }
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
			Validation struct{ Ok bool }
		}
	}
	mustPost(t, c, `query($p: JSON!) { previewPlan(input: {plan: $p}) {
		report { changes { kind entities } } validation { ok } } }`, &preview,
		client.Var("p", map[string]any{"sync": map[string]any{"removeTables": true}}))
	ch := preview.PreviewPlan.Report.Changes
	if len(ch) != 1 || ch[0].Kind != "removeTable" || len(ch[0].Entities) != 1 || !preview.PreviewPlan.Validation.Ok {
		t.Fatalf("preview = %+v", preview.PreviewPlan)
	}
}
