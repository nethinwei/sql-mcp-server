package configapp

import (
	"reflect"
	"testing"

	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/configplan"
)

// A plan written in another order, or naming a value twice, normalizes to
// the same plan (and so gets the same plan ID).
func TestNormalizedPlanIgnoresOrder(t *testing.T) {
	who := auth.Principal{Username: "root", Permissions: []string{auth.PermAll}}
	plan := func(ds, schemas, excluded []string, actions ...string) configplan.Plan {
		return configplan.Plan{
			Scope:   configplan.Scope{Datasources: ds, Schemas: schemas, Status: []configplan.Status{"new", "workspace"}},
			Grants:  []configplan.GrantRule{{Principal: "role:bi", Actions: actions}},
			Exclude: excluded,
		}
	}
	a := plan([]string{"b", "a"}, []string{"s*", "public"}, []string{"a.public.t2", "a.public.t1"}, "Read", "aggregate")
	b := plan([]string{"a", "b", "a"}, []string{"public", "s*"}, []string{"a.public.t1", "a.public.t2"},
		"aggregate", "read")
	b.Scope.Status = []configplan.Status{"workspace", "new"}
	for _, p := range []*configplan.Plan{&a, &b} {
		if err := normalized(who, p); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("plans differ:\n%+v\n%+v", a, b)
	}
}
