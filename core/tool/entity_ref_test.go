package tool

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
)

// namespacedTenants registers two entities named tenants, in different
// datasources; role "both" can read both, role "ops" only the warehouse one.
func namespacedTenants(t *testing.T) (*entity.Registry, rbac.Authorizer) {
	t.Helper()
	tenants := func(datasource, schema string) entity.Entity {
		return entity.Entity{
			Name: entity.ID(datasource, schema, "tenants"), Local: "tenants", DataSource: datasource, Schema: schema,
			Source: "tenants", Attributes: []entity.Attribute{{Name: "id"}}, MCP: entity.MCPFlags{DMLTools: true},
		}
	}
	shop, warehouse := tenants("shop", "crm"), tenants("warehouse", "logistics")
	reg, err := entity.NewRegistry([]entity.Entity{shop, warehouse})
	if err != nil {
		t.Fatal(err)
	}
	read := []rbac.Grant{{ID: "g", Actions: []entity.Action{entity.ActionRead}}}
	return reg, rbac.NewGrantAuthorizer(reg, rbac.Policy{Roles: map[string]map[string][]rbac.Grant{
		"both": {shop.Name: read, warehouse.Name: read},
		"ops":  {warehouse.Name: read},
	}})
}

func TestEntityReferencesResolveAmongUsableEntities(t *testing.T) {
	t.Parallel()
	reg, authz := namespacedTenants(t)
	const warehouse = "warehouse.logistics.tenants"
	describe := func(role, ref string) ([]map[string]any, error) {
		input, _ := json.Marshal(map[string]string{"entity": ref})
		res, err := RunTool(context.Background(), DescribeTool{}, input,
			Context{Role: role, Registry: reg, Authorizer: authz})
		return res.Content, err
	}

	// Both entities are usable: the bare name is ambiguous, and the
	// candidates name each one unambiguously.
	_, err := describe("both", "tenants")
	var ambiguous *AmbiguousEntityError
	if !errors.As(err, &ambiguous) || !slices.Equal(ambiguous.Candidates, []string{"crm.tenants", "logistics.tenants"}) {
		t.Fatalf("ambiguous reference: %v", err)
	}
	if d, ok := DenialFor(err, "d"); !ok || d.Code != CodeAmbiguousEntity {
		t.Fatalf("denial = %+v", d)
	}
	got, err := describe("both", "warehouse.tenants")
	if err != nil || got[0]["id"] != warehouse || got[0]["name"] != "logistics.tenants" {
		t.Fatalf("qualified reference: %v %v", got, err)
	}

	// An entity the caller cannot use neither makes the name ambiguous nor
	// shows up anywhere.
	got, err = describe("ops", "tenants")
	if err != nil || got[0]["id"] != warehouse || got[0]["name"] != "tenants" {
		t.Fatalf("bare name for ops: %v %v", got, err)
	}
	got, err = describe("ops", "")
	if err != nil || len(got) != 1 || got[0]["name"] != "tenants" {
		t.Fatalf("listing for ops: %v %v", got, err)
	}
}

func TestOnlyEntityToolsHaveTheirEntityResolved(t *testing.T) {
	t.Parallel()
	reg, authz := namespacedTenants(t)
	tc := Context{Role: "ops", Registry: reg, Authorizer: authz}
	input := json.RawMessage(`{"entity":"tenants"}`)
	got, err := canonicalEntity(context.Background(), ReadTool{}, input, tc)
	if err != nil || !strings.Contains(string(got), `"warehouse.logistics.tenants"`) {
		t.Fatalf("read_records input = %s, %v", got, err)
	}
	// A procedure parameter named entity is a value, passed as given.
	procedure := ProcedureTool{Entity: entity.Entity{
		Name: "shop.close", Kind: entity.KindProcedure, Params: []string{"entity"},
	}}
	got, err = canonicalEntity(context.Background(), procedure, input, tc)
	if err != nil || string(got) != string(input) {
		t.Fatalf("procedure input = %s, %v", got, err)
	}
}
