package graph

import (
	"reflect"
	"testing"

	"github.com/nethinwei/sql-mcp-server/x/admin/configapp"
)

func TestVisibilityPageCountsFiltersAndPages(t *testing.T) {
	result := func(allowed bool, scopes ...[]string) *configapp.Simulation {
		return &configapp.Simulation{Allowed: allowed, FieldScopes: scopes}
	}
	all := []EntityVisibility{
		{Entity: "shop.orders", Actions: []ActionVisibility{
			{Action: "READ", Result: result(true)}, {Action: "DELETE", Result: result(false)}}},
		{Entity: "shop.customers", Actions: []ActionVisibility{
			{Action: "READ", Result: result(false, []string{"id"}, []string{"email"})}}},
		{Entity: "shop.refunds", Actions: []ActionVisibility{
			{Action: "READ", Result: result(false, []string{})}}},
		{Entity: "shop.close_ticket", Actions: []ActionVisibility{{Action: "EXECUTE", Result: result(false)}}},
	}
	ids := func(p *VisibilityPage) []string {
		out := []string{}
		for _, ev := range p.Items {
			out = append(out, ev.Entity)
		}
		return out
	}
	page := visibilityPage(all, nil, 0, 2)
	if page.Total != 4 || page.Reachable != 2 || page.NeedsFields != 1 || page.Matched != 4 {
		t.Fatalf("counts = %+v", page)
	}
	if want := []string{"READ", "DELETE", "EXECUTE"}; !reflect.DeepEqual(page.Actions, want) {
		t.Fatalf("actions = %v, want %v", page.Actions, want)
	}
	if got := ids(page); !reflect.DeepEqual(got, []string{"shop.orders", "shop.customers"}) {
		t.Fatalf("first page = %v", got)
	}
	if got := ids(visibilityPage(all, nil, 2, 2)); !reflect.DeepEqual(got, []string{"shop.refunds", "shop.close_ticket"}) {
		t.Fatalf("second page = %v", got)
	}
	reachable, search := true, "CUST"
	page = visibilityPage(all, &VisibilityFilter{Search: &search, ReachableOnly: &reachable}, 0, 50)
	if page.Matched != 1 || page.Total != 4 || !reflect.DeepEqual(ids(page), []string{"shop.customers"}) {
		t.Fatalf("filtered = %+v", page)
	}
	if page = visibilityPage(all, nil, 10, 1000); page.Matched != 4 || len(page.Items) != 0 {
		t.Fatalf("past the end = %+v", page)
	}
}
