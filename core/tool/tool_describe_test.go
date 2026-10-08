package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
)

func TestDescribeTool(t *testing.T) {
	t.Parallel()
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	tc := Context{Role: "reader", Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg)}
	in, _ := json.Marshal(map[string]any{"entity": "users"})
	res, err := DescribeTool{}.Run(context.Background(), in, tc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content[0]["name"] != "users" || res.Content[0]["datasource"] != "default" {
		t.Fatalf("got %v", res.Content[0])
	}
}

func TestDescribeToolFiltersUnauthorizedEntitiesAndFields(t *testing.T) {
	t.Parallel()
	users := testUsersEntity()
	users.Attributes = append(users.Attributes, entity.Attribute{Name: "secret"})
	users.FieldAccess = entity.FieldAccess{"reader": {Read: []string{"id"}}}
	adminOnly := entity.Entity{
		Name: "admin_only", Source: "admin_only", Attributes: []entity.Attribute{{Name: "id"}},
		Role: entity.RoleAccess{entity.ActionRead: {"admin"}}, MCP: entity.MCPFlags{DMLTools: true},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{users, adminOnly})
	tc := Context{Role: "reader", Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg)}
	list, err := DescribeTool{}.Run(context.Background(), json.RawMessage(`{}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Content) != 1 || list.Content[0]["name"] != "users" {
		t.Fatalf("entity list leaked unauthorized entity: %#v", list.Content)
	}
	detail, err := DescribeTool{}.Run(context.Background(), json.RawMessage(`{"entity":"users"}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	fields := detail.Content[0]["fields"].([]map[string]any)
	if len(fields) != 1 || fields[0]["name"] != "id" {
		t.Fatalf("field list leaked ACL fields: %#v", fields)
	}
	if _, err := (DescribeTool{}).Run(context.Background(), json.RawMessage(`{"entity":"admin_only"}`), tc); !errors.Is(
		err,
		ErrUnauthorized,
	) {
		t.Fatalf("unauthorized detail error = %v", err)
	}
}

// Read grants with split field scopes deny the default projection, but the
// entity stays discoverable, like in the authorized-schema resource, with the
// fields that an explicit selection may use.
func TestDescribeToolListsSplitFieldScopes(t *testing.T) {
	t.Parallel()
	users := testUsersEntity()
	users.Role = nil
	users.Attributes = []entity.Attribute{{Name: "id"}, {Name: "name"}, {Name: "phone"}, {Name: "secret"}}
	reg, _ := entity.NewRegistry([]entity.Entity{users})
	read := func(id string, fields ...string) rbac.Grant {
		return rbac.Grant{ID: id, Actions: []entity.Action{entity.ActionRead},
			Fields: &entity.FieldPermissions{Read: fields}}
	}
	policy := rbac.Policy{Roles: map[string]map[string][]rbac.Grant{
		"reader": {"users": {read("n", "id", "name"), read("p", "id", "phone")}},
	}}
	tc := Context{Role: "reader", Registry: reg, Authorizer: rbac.NewGrantAuthorizer(reg, policy)}
	list, err := DescribeTool{}.Run(context.Background(), json.RawMessage(`{}`), tc)
	if err != nil || len(list.Content) != 1 || list.Content[0]["name"] != "users" {
		t.Fatalf("list = %#v, %v", list.Content, err)
	}
	detail, err := DescribeTool{}.Run(context.Background(), json.RawMessage(`{"entity":"users"}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range detail.Content[0]["fields"].([]map[string]any) {
		names = append(names, f["name"].(string))
	}
	if strings.Join(names, ",") != "id,name,phone" {
		t.Fatalf("fields = %v", names)
	}
}

func TestDescribeRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	tc := Context{Role: "reader", Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg)}
	for _, input := range []string{`{"entitty":"users"}`, `[]`, `{"entity":1}`} {
		if _, err := (DescribeTool{}).Run(context.Background(), json.RawMessage(input), tc); !errors.Is(
			err, ErrInvalidInput,
		) {
			t.Errorf("input %s: error = %v, want invalid input", input, err)
		}
	}
}

func TestDescribeReportsKeysAndWritability(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name: "orders", Source: "orders", MCP: entity.MCPFlags{DMLTools: true},
		Attributes: []entity.Attribute{
			{Name: "id", Domain: entity.Domain{Default: true, AutoIncrement: true}},
			{Name: "order_no"},
			{Name: "note", Domain: entity.Domain{Nullable: true}},
			{Name: "total_cents", Domain: entity.Domain{Generated: true}},
			{Name: "secret_ref", Excluded: true},
		},
		Keys: []entity.Key{
			{Name: "pk", Columns: []string{"id"}, Primary: true},
			{Name: "uk_no", Columns: []string{"order_no"}},
			{Name: "uk_ref", Columns: []string{"secret_ref"}},
			{Name: "uk_note", Columns: []string{"note"}, Reason: entity.KeyNullable},
		},
		Role: entity.RoleAccess{entity.ActionRead: {"reader"}},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	tc := Context{Role: "reader", Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg)}
	res, err := DescribeTool{}.Run(context.Background(), json.RawMessage(`{"entity":"orders"}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := json.Marshal(res.Content[0]["keys"])
	if string(keys) != `[["id"],["order_no"]]` {
		t.Fatalf("keys = %s, want the identity keys visible to the caller", keys)
	}
	fields := map[string]map[string]any{}
	for _, f := range res.Content[0]["fields"].([]map[string]any) {
		fields[f["name"].(string)] = f
	}
	for name, want := range map[string][2]bool{
		"id": {false, false}, "order_no": {true, false}, "note": {false, false}, "total_cents": {false, true},
	} {
		if fields[name]["required"] != want[0] || fields[name]["readOnly"] != want[1] {
			t.Errorf("%s: required=%v readOnly=%v, want %v", name, fields[name]["required"], fields[name]["readOnly"], want)
		}
	}
}
