package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
)

// largeDescribe is a deployment of 3000 entities of 20 fields over 10
// schemas, a role granted a third of them with a field list each.
func largeDescribe(b *testing.B) Context {
	b.Helper()
	var entities []entity.Entity
	grants := map[string][]rbac.Grant{}
	for i := range 3000 {
		e := entity.Entity{Name: fmt.Sprintf("t%04d", i), Source: fmt.Sprintf("t%04d", i),
			Schema: fmt.Sprintf("s%d", i%10), Description: fmt.Sprintf("table %d", i),
			Keys: []entity.Key{{Columns: []string{"c0"}, Primary: true}}, MCP: entity.MCPFlags{DMLTools: true}}
		for f := range 20 {
			e.Attributes = append(e.Attributes, entity.Attribute{Name: fmt.Sprintf("c%d", f)})
		}
		entities = append(entities, e)
		if i%3 == 0 {
			grants[e.Name] = []rbac.Grant{{ID: e.Name, Actions: []entity.Action{entity.ActionRead},
				Fields: &entity.FieldPermissions{Read: []string{"c0", "c1", "c2", "c19"}}}}
		}
	}
	reg, err := entity.NewRegistry(entities)
	if err != nil {
		b.Fatal(err)
	}
	authz := rbac.NewGrantAuthorizer(reg, rbac.Policy{Roles: map[string]map[string][]rbac.Grant{"analyst": grants}})
	return Context{Services: &Services{Registry: reg, Authorizer: authz}, Role: "analyst"}
}

// BenchmarkDescribeList lists a page of the entities a role may use, as an
// agent does first.
func BenchmarkDescribeList(b *testing.B) {
	tc := largeDescribe(b)
	b.ReportAllocs()
	for b.Loop() {
		res, err := DescribeTool{}.Run(context.Background(), json.RawMessage(`{"search":"table 1"}`), tc)
		if err != nil || res.Content[0]["total"] != 369 {
			b.Fatalf("total %v: %v", res.Content[0]["total"], err)
		}
	}
}

// BenchmarkAuthorizeRead authorizes reading fields of one entity, as every
// read does.
func BenchmarkAuthorizeRead(b *testing.B) {
	tc := largeDescribe(b)
	req := rbac.Request{Role: "analyst", Entity: "t0300", Action: entity.ActionRead, Fields: []string{"c0", "c19"}}
	b.ReportAllocs()
	for b.Loop() {
		if dec, err := tc.Authorizer.Authorize(context.Background(), req); err != nil || !dec.Allowed {
			b.Fatal(dec, err)
		}
	}
}
