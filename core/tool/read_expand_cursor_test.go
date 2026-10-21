package tool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/core/store/storetest"
	"github.com/nethinwei/sql-mcp-server/internal/testdialect"
)

// A full page ordered by a join column an expansion reads, though not asked
// for, still says where the next one starts.
func TestExpandOrderedByItsJoinColumnPages(t *testing.T) {
	parent := testUsersEntity()
	parent.DataSource = "default"
	parent.Relations = []entity.Relationship{{
		Name: "orders", Target: "orders", Cardinality: "many",
		JoinOn: map[string]string{"id": "user_id"},
	}}
	orders := entity.Entity{
		Name: "orders", Source: "orders", DataSource: "default",
		Attributes: []entity.Attribute{{Name: "id"}, {Name: "user_id"}},
		Role:       entity.RoleAccess{entity.ActionRead: {"reader"}},
		MCP:        entity.MCPFlags{DMLTools: true},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{parent, orders})
	db := &storetest.FakeDB{}
	queries := 0
	db.QueryFn = func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
		queries++
		if queries == 1 {
			return storetest.NewFakeRows([]string{"id", "email"},
				[]any{int64(1), "a@x.com"}, []any{int64(2), "b@x.com"}), nil
		}
		return storetest.NewFakeRows([]string{"id", "user_id"}, []any{int64(10), int64(1)}), nil
	}
	tc := Context{Role: "reader", DB: db, Dialect: testdialect.Postgres{}, Services: &Services{
		Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg),
		Sources: map[string]DataSource{"default": {DB: db, Dialect: testdialect.Postgres{}}},
	}}
	input, _ := json.Marshal(readInput{Entity: "users", Fields: []string{"email"}, Expand: []string{"orders"},
		OrderBy: []orderJSON{{Field: "id"}}, Limit: 2})
	result, err := ReadTool{}.Run(context.Background(), input, tc)
	if err != nil {
		t.Fatal(err)
	}
	if result.NextCursor["id"] != int64(2) {
		t.Fatalf("next cursor = %v, truncated %v", result.NextCursor, result.Truncated)
	}
	if _, ok := result.Content[0]["id"]; ok {
		t.Fatalf("the join column was not asked for: %v", result.Content[0])
	}
}
