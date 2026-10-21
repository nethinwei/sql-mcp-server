package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/budget"
	"github.com/nethinwei/sql-mcp-server/core/cost"
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

// A child many parent rows name is counted once as it is read, but returned
// once per parent: the result's byte limit holds for what is returned.
func TestExpandedChildrenCountPerParentAgainstTheByteLimit(t *testing.T) {
	t.Parallel()
	orders := entity.Entity{
		Name: "orders", Source: "orders", DataSource: "default",
		Attributes: []entity.Attribute{{Name: "id"}, {Name: "team_id"}},
		Keys:       []entity.Key{{Columns: []string{"id"}, Primary: true}},
		Relations: []entity.Relationship{{Name: "team", Target: "teams", Cardinality: "belongs-to",
			JoinOn: map[string]string{"team_id": "id"}}},
		Role: entity.RoleAccess{entity.ActionRead: {"reader"}},
		MCP:  entity.MCPFlags{DMLTools: true},
	}
	teams := entity.Entity{
		Name: "teams", Source: "teams", DataSource: "default",
		Attributes: []entity.Attribute{{Name: "id"}, {Name: "bio"}},
		Keys:       []entity.Key{{Columns: []string{"id"}, Primary: true}},
		Role:       entity.RoleAccess{entity.ActionRead: {"reader"}},
		MCP:        entity.MCPFlags{DMLTools: true},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{orders, teams})
	db := &storetest.FakeDB{QueryFn: func(_ context.Context, query string, _ ...any) (store.Rows, error) {
		if strings.Contains(query, `"teams"`) {
			return storetest.NewFakeRows([]string{"id", "bio"}, []any{int64(1), strings.Repeat("x", 200)}), nil
		}
		var rows [][]any
		for i := range 30 {
			rows = append(rows, []any{int64(i), int64(1)})
		}
		return storetest.NewFakeRows([]string{"id", "team_id"}, rows...), nil
	}}
	tc := Context{Role: "reader", DB: db, Dialect: testdialect.Postgres{}, Services: &Services{
		Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg),
		Sources: map[string]DataSource{"default": {DB: db, Dialect: testdialect.Postgres{}}},
	}}
	input, _ := json.Marshal(readInput{Entity: "orders", Expand: []string{"team"}})
	result, err := RunTool(context.Background(), ReadTool{}, input, tc)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result.Content)
	if result.ReturnedBytes < int64(len(encoded)) {
		t.Fatalf("returned bytes = %d, the result encodes to %d", result.ReturnedBytes, len(encoded))
	}
	tc.MaxReturnedBytes = 3000 // the rows read are about 1 KB, returned about 7 KB
	if _, err := RunTool(context.Background(), ReadTool{}, input, tc); !errors.Is(err, budget.ErrExceeded) {
		t.Fatalf("err = %v, want %v", err, budget.ErrExceeded)
	}
}

// An expansion's batch reads one row past the row limit: a batch of more
// children than the limit is refused rather than answered with parents
// missing them, one of exactly as many is complete. Only when the gate cut
// the batch at the limit (cost.maxRows no larger than the caller's) can a
// full batch not be told from a cut one, and it is refused. Without a row
// limit (maxRows 0) a batch the gate cut is not refused.
func TestExpandedChildrenAtTheRowLimitAreRefused(t *testing.T) {
	t.Parallel()
	keyed := func(name string, fields ...string) entity.Entity {
		e := entity.Entity{Name: name, Source: name, DataSource: "default",
			Keys: []entity.Key{{Columns: []string{"id"}, Primary: true}},
			Role: entity.RoleAccess{entity.ActionRead: {"reader"}}, MCP: entity.MCPFlags{DMLTools: true}}
		for _, f := range append([]string{"id"}, fields...) {
			e.Attributes = append(e.Attributes, entity.Attribute{Name: f})
		}
		return e
	}
	orders := keyed("orders")
	orders.Relations = []entity.Relationship{{Name: "items", Target: "items", Cardinality: "has-many",
		JoinOn: map[string]string{"id": "order_id"}}}
	reg, _ := entity.NewRegistry([]entity.Entity{orders, keyed("items", "order_id")})
	input, _ := json.Marshal(readInput{Entity: "orders", Expand: []string{"items"}, Limit: 2})
	looser, atTheLimit := cost.NewGate(cost.EnforceCap{HardRows: 10}), cost.NewGate(cost.EnforceCap{HardRows: 3})
	for _, c := range []struct {
		gate              cost.Gate // cost.maxRows; nil: none
		children, maxRows int64     // maxRows: the caller's
		want              error
		limit             string // the batch's
	}{
		{nil, 3, 3, nil, " LIMIT 4"}, {nil, 4, 3, budget.ErrExceeded, " LIMIT 4"}, {looser, 3, 3, nil, " LIMIT 4"},
		{looser, 4, 3, budget.ErrExceeded, " LIMIT 4"}, {atTheLimit, 2, 3, nil, " LIMIT 3"},
		{atTheLimit, 3, 3, budget.ErrExceeded, " LIMIT 3"}, {atTheLimit, 0, 0, nil, " LIMIT 3"},
	} {
		db := &storetest.FakeDB{QueryFn: func(_ context.Context, query string, _ ...any) (store.Rows, error) {
			if strings.Contains(query, `"items"`) {
				var rows [][]any
				for i := range c.children {
					rows = append(rows, []any{int64(i), int64(1 + i%2)})
				}
				return storetest.NewFakeRows([]string{"id", "order_id"}, rows...), nil
			}
			return storetest.NewFakeRows([]string{"id"}, []any{int64(1)}, []any{int64(2)}), nil
		}}
		source := DataSource{DB: db, Dialect: testdialect.Postgres{}, Gate: c.gate}
		tc := Context{Role: "reader", DB: db, Dialect: testdialect.Postgres{}, Services: &Services{
			Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg), Sources: map[string]DataSource{"default": source},
		}}
		tc.MaxRows = c.maxRows
		if _, err := RunTool(context.Background(), ReadTool{}, input, tc); !errors.Is(err, c.want) {
			t.Errorf("%+v: err = %v", c, err)
		}
		if sql := db.Queries[len(db.Queries)-1].Query; !strings.HasSuffix(sql, c.limit) {
			t.Errorf("%+v: %s", c, sql)
		}
	}
}
