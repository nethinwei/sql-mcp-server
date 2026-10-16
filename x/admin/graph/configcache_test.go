package graph

import (
	"context"
	"reflect"
	"testing"

	"github.com/99designs/gqlgen/client"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/admin/configapp"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
)

// The configuration a revision selects is parsed once and shared, and what
// reads it (visibility, simulation) leaves it as parsed.
func TestSelectedConfigurationIsKeptAndLeftUnchanged(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := "1"
	first, err := h.resolver.App.SelectConfig(ctx, configapp.Against{Revision: &id})
	if err != nil {
		t.Fatal(err)
	}
	published, err := h.resolver.App.SelectConfig(ctx, configapp.Against{})
	if err != nil || published != first {
		t.Fatalf("the published revision 1 was parsed again (%v)", err)
	}
	c := h.client(auth.PermRead)
	var vis struct{ VisibilityPage struct{ Total int } }
	mustPost(t, c, `query { visibilityPage(input: {user: "alice", revision: "1"}) { total } }`, &vis)
	var sim struct{ Simulate struct{ Allowed bool } }
	mustPost(t, c, `query($i: SimulationInput!) { simulate(input: $i) { allowed } }`, &sim,
		client.Var("i", map[string]any{"user": "alice", "entity": "customers", "action": "READ", "revision": "1"}))
	rev, err := h.store.Get(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := bootstrap.LoadRevision(rev)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := h.resolver.App.SelectConfig(ctx, configapp.Against{Revision: &id}); again != first ||
		!reflect.DeepEqual(first, fresh) {
		t.Fatal("the kept configuration was parsed again or changed by what read it")
	}
}
