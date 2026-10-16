package tool

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/cost/costtest"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/internal/testdialect"
)

func indexedEntity() entity.Entity {
	return entity.Entity{
		Name: "orders", Source: "orders",
		Attributes: []entity.Attribute{{Name: "id"}, {Name: "tenant"}, {Name: "created_at"},
			{Name: "email", Mask: "email"}, {Name: "title"}, {Name: "secret", Excluded: true}},
		Keys: []entity.Key{{Columns: []string{"id"}, Primary: true}},
		Indexes: []entity.Index{
			{Name: "orders_pkey", Method: "btree", Parts: []string{"id"}, Unique: true, Primary: true},
			{Name: "ix_tenant_created", Method: "btree", Parts: []string{"tenant", "created_at"}},
			{Name: "ix_email", Method: "btree", Parts: []string{"email", "id"}},
			{Name: "ix_title", Method: "btree", Parts: []string{"title(20)"}},
			{Name: "ix_lower", Method: "btree", Parts: []string{"lower(title)"}},
			{Name: "ux_tenant_secret", Method: "btree", Parts: []string{"tenant", "secret"}, Unique: true,
				Where: "secret IS NOT NULL"},
		},
		Role: entity.RoleAccess{entity.ActionRead: {"reader"}},
		MCP:  entity.MCPFlags{DMLTools: true},
	}
}

// describe_entities lists the indexes a caller can filter by: masked,
// excluded and unseen columns cut an index short or leave it out, a column
// prefix counts as its column, expressions and partial predicates are not
// shown.
func TestDescribeListsVisibleIndexes(t *testing.T) {
	t.Parallel()
	reg, _ := entity.NewRegistry([]entity.Entity{indexedEntity()})
	tc := Context{Services: &Services{Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg)}, Role: "reader"}
	res, err := DescribeTool{}.Run(context.Background(), json.RawMessage(`{"entity":"orders"}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	got := res.Content[0]["indexes"].([]map[string]any)
	want := []map[string]any{
		{"name": "orders_pkey", "fields": []string{"id"}, "method": "btree", "unique": true, "partial": false},
		{"name": "ix_tenant_created", "fields": []string{"tenant", "created_at"}, "method": "btree",
			"unique": false, "partial": false},
		{"name": "ix_title", "fields": []string{"title"}, "method": "btree", "unique": false, "partial": false},
		{"name": "ux_tenant_secret", "fields": []string{"tenant"}, "method": "btree", "unique": false,
			"partial": true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("indexes = %v\nwant %v", got, want)
	}
}

// A read the cost gate refuses names the indexed fields to filter on, and
// the limits and estimate it was judged by.
func TestCostRefusalNamesIndexedFields(t *testing.T) {
	t.Parallel()
	reg, _ := entity.NewRegistry([]entity.Entity{indexedEntity()})
	gate := cost.NewGate(cost.Estimate{
		Explainer: costtest.FakeExplainer{Plan: cost.Plan{ScanType: cost.ScanIndex, EstimatedRows: 11_482_410,
			StatsFresh: true}},
		Threshold: cost.Threshold{SoftScore: 60, HardScore: 40, MaxScannedRows: 100_000},
	})
	tc := Context{
		Services: &Services{Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg)},
		Role:     "reader", Dialect: testdialect.Postgres{}, Gate: gate,
	}
	_, err := ReadTool{}.Run(context.Background(), json.RawMessage(`{"entity":"orders","fields":["id"]}`), tc)
	d, ok := DenialFor(err, "")
	if !ok || d.Code != CodeCostExceeded {
		t.Fatalf("denial = %+v, %v", d, err)
	}
	hints := strings.Join(d.Hints, "\n")
	for _, want := range []string{"id, (tenant, created_at), title, tenant", "over the limit of 100000"} {
		if !strings.Contains(hints, want) {
			t.Errorf("hints lack %q:\n%s", want, hints)
		}
	}
	if d.Constraints["estimatedScannedRows"] != int64(11_482_410) || d.Constraints["maxScannedRows"] != int64(100_000) {
		t.Errorf("constraints = %v", d.Constraints)
	}
}
