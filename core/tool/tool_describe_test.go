package tool

import (
	"context"
	"encoding/json"
	"errors"
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
	if res.Content[0]["name"] != "users" {
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
