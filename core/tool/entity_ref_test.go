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
			Context{Services: &Services{Registry: reg, Authorizer: authz}, Role: role})
		if ref == "" && err == nil {
			return listedEntities(res), nil
		}
		return res.Content, err
	}

	// Both entities are usable: the bare name is ambiguous, and the
	// candidates name each one unambiguously.
	_, err := describe("both", "tenants")
	var ambiguous *AmbiguousEntityError
	if !errors.As(err, &ambiguous) ||
		!slices.Equal(ambiguous.Candidates, []string{"crm.tenants", "logistics.tenants"}) {
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
	tc := Context{Services: &Services{Registry: reg, Authorizer: authz}, Role: "ops"}
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

// A call naming its entity ambiguously is refused, and audited with the
// masked fields of every entity it may name redacted.
func TestAmbiguousReferenceIsAuditedRedacted(t *testing.T) {
	t.Parallel()
	tenants := func(datasource, schema string) entity.Entity {
		return entity.Entity{
			Name: entity.ID(datasource, schema, "tenants"), Local: "tenants", DataSource: datasource, Schema: schema,
			Source: "tenants", MCP: entity.MCPFlags{DMLTools: true},
			Attributes: []entity.Attribute{{Name: "id"}, {Name: "secret", Mask: "full"}},
		}
	}
	shop, warehouse := tenants("shop", "crm"), tenants("warehouse", "logistics")
	reg, err := entity.NewRegistry([]entity.Entity{shop, warehouse})
	if err != nil {
		t.Fatal(err)
	}
	write := []rbac.Grant{{ID: "g", Actions: []entity.Action{entity.ActionCreate}}}
	authz := rbac.NewGrantAuthorizer(reg, rbac.Policy{Roles: map[string]map[string][]rbac.Grant{
		"both": {shop.Name: write, warehouse.Name: write},
	}})
	aud := &recorderAuditor{}
	input, _ := json.Marshal(createInput{Entity: "tenants", Values: map[string]any{"id": 1, "secret": "hunter2"}})
	_, err = RunTool(context.Background(), CreateTool{}, input,
		Context{Services: &Services{Registry: reg, Authorizer: authz, Auditor: aud}, Role: "both"})
	var ambiguous *AmbiguousEntityError
	if !errors.As(err, &ambiguous) || len(aud.events) != 1 {
		t.Fatalf("err = %v, events = %+v", err, aud.events)
	}
	if got := string(aud.events[0].Input); strings.Contains(got, "hunter2") {
		t.Fatalf("audited %s", got)
	}
}

// A call naming an entity that does not resolve (a typo, or one a reload
// removed) cannot tell its masked fields: it is audited with every field
// value redacted, ambiguous or unknown alike.
func TestUnresolvedReferenceIsAuditedRedacted(t *testing.T) {
	t.Parallel()
	reg, err := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		`{"entity":"patients","values":{"id":1,"ssn":"078-05-1120"}}`,
		`{"entity":"","values":{"id":1,"ssn":"078-05-1120"}}`,
		`{"entity":"patients","filter":[{"field":"ssn","op":"eq","value":"078-05-1120"}]}`,
	} {
		aud := &recorderAuditor{}
		_, _ = RunTool(context.Background(), CreateTool{}, json.RawMessage(input),
			Context{Services: &Services{Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg), Auditor: aud},
				Role: "writer"})
		if len(aud.events) != 1 || strings.Contains(string(aud.events[0].Input), "078-05-1120") {
			t.Fatalf("%s audited %+v", input, aud.events)
		}
	}
	// A call naming no entity has no field values to tell apart: kept.
	if got := sensitiveFields("list_entities", json.RawMessage(`{}`), reg); len(got) != 0 {
		t.Fatalf("sensitive fields of a call naming no entity = %v", got)
	}
}
