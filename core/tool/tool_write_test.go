package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/hook"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/internal/testdialect"
)

func TestCreateTool(t *testing.T) {
	t.Parallel()
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	auth := rbac.NewRoleAuthorizer(reg)
	db := &store.FakeDB{ExecFn: func(_ context.Context, _ string, _ ...any) (store.Result, error) {
		return store.Result{RowsAffected: 1, LastInsertID: 42}, nil
	}}
	tc := Context{Role: "writer", DB: db, Dialect: testdialect.MySQL{}, Registry: reg, Authorizer: auth}
	in, _ := json.Marshal(createInput{Entity: "users", Values: map[string]any{"email": "bob@x.com"}})
	res, err := CreateTool{}.Run(context.Background(), in, tc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content[0]["rowsAffected"] != int64(1) {
		t.Fatalf("got %v", res.Content[0])
	}
	if len(db.Execs) != 1 || db.Execs[0].Query == "" {
		t.Fatalf("exec not logged: %v", db.Execs)
	}
}

func TestExecErrorsAreWrapped(t *testing.T) {
	t.Parallel()
	cause := errors.New("driver exec failed")
	e := entity.Entity{
		Name:       "users",
		Source:     "users",
		Attributes: []entity.Attribute{{Name: "id"}},
		Keys:       []entity.Key{{Columns: []string{"id"}, Primary: true}},
		Role:       entity.RoleAccess{entity.ActionUpdate: {"writer"}},
		MCP:        entity.MCPFlags{DMLTools: true},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	db := &store.FakeDB{ExecFn: func(context.Context, string, ...any) (store.Result, error) {
		return store.Result{}, cause
	}}
	tc := Context{
		Role: "writer", DB: db, Dialect: testdialect.Postgres{}, Registry: reg,
		Authorizer: rbac.NewRoleAuthorizer(reg),
	}
	_, err := (UpdateTool{}).Run(context.Background(), json.RawMessage(
		`{"entity":"users","filter":[{"field":"id","op":"eq","value":1}],"set":{"id":2}}`,
	), tc)
	if !errors.Is(err, ErrDatabase) || !errors.Is(err, cause) {
		t.Fatalf("exec error = %v", err)
	}
}

func TestUpdateToolUnsafeWrite(t *testing.T) {
	t.Parallel()
	e := testUsersEntity()
	e.Role[entity.ActionUpdate] = []string{"writer"}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	auth := rbac.NewRoleAuthorizer(reg)
	tc := Context{Role: "writer", Dialect: testdialect.Postgres{}, Registry: reg, Authorizer: auth}
	// no filter -> unsafe
	in, _ := json.Marshal(updateInput{Entity: "users", Set: map[string]any{"email": "x@x.com"}})
	_, err := UpdateTool{}.Run(context.Background(), in, tc)
	if !errors.Is(err, ErrUnsafeWrite) {
		t.Fatalf("got %v, want ErrUnsafeWrite", err)
	}
}

func TestCreateToolReturning(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name: "users", Source: "users", Kind: entity.KindTable,
		Attributes: []entity.Attribute{{Name: "id"}, {Name: "email"}},
		Keys:       []entity.Key{{Columns: []string{"id"}, Primary: true}},
		Role:       entity.RoleAccess{entity.ActionCreate: {"writer"}},
		MCP:        entity.MCPFlags{DMLTools: true},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	auth := rbac.NewRoleAuthorizer(reg)
	db := &store.FakeDB{QueryFn: func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
		return store.NewFakeRows([]string{"id"}, []any{int64(7)}), nil
	}}
	tc := Context{Role: "writer", DB: db, Dialect: testdialect.Postgres{}, Registry: reg, Authorizer: auth}
	in, _ := json.Marshal(createInput{Entity: "users", Values: map[string]any{"email": "a@x.com"}})
	res, err := CreateTool{}.Run(context.Background(), in, tc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content[0]["id"] != int64(7) {
		t.Fatalf("got %v", res.Content[0])
	}
}

// With several datasources only the routed context has a dialect; the insert
// must use it rather than the top-level one.
func TestCreateToolUsesTheRoutedDatasource(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name: "refunds", Source: "refunds", Kind: entity.KindTable, DataSource: "shop",
		Attributes: []entity.Attribute{{Name: "id"}, {Name: "amount"}},
		Keys:       []entity.Key{{Columns: []string{"id"}, Primary: true}},
		Role:       entity.RoleAccess{entity.ActionCreate: {"writer"}},
		MCP:        entity.MCPFlags{DMLTools: true},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	shop := &store.FakeDB{QueryFn: func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
		return store.NewFakeRows([]string{"id"}, []any{int64(9)}), nil
	}}
	tc := Context{
		Role: "writer", Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg),
		Sources: map[string]DataSource{
			"shop":      {DB: shop, Dialect: testdialect.Postgres{}},
			"analytics": {DB: &store.FakeDB{}, Dialect: testdialect.Postgres{}},
		},
	}
	in, _ := json.Marshal(createInput{Entity: "refunds", Values: map[string]any{"amount": "10.00"}})
	res, err := CreateTool{}.Run(context.Background(), in, tc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content[0]["id"] != int64(9) {
		t.Fatalf("got %v", res.Content[0])
	}
}

func TestDeleteTool(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name: "users", Source: "users",
		Attributes: []entity.Attribute{{Name: "id"}},
		Keys:       []entity.Key{{Columns: []string{"id"}, Primary: true}},
		Role:       entity.RoleAccess{entity.ActionDelete: {"admin"}},
		MCP:        entity.MCPFlags{DMLTools: true},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	auth := rbac.NewRoleAuthorizer(reg)
	db := &store.FakeDB{ExecFn: func(_ context.Context, _ string, _ ...any) (store.Result, error) {
		return store.Result{RowsAffected: 1}, nil
	}}
	tc := Context{Role: "admin", DB: db, Dialect: testdialect.Postgres{}, Registry: reg, Authorizer: auth}
	in, _ := json.Marshal(deleteInput{Entity: "users", Filter: []condJSON{{Field: "id", Op: "eq", Value: 1}}})
	res, err := DeleteTool{}.Run(context.Background(), in, tc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content[0]["rowsAffected"] != int64(1) {
		t.Fatalf("got %v", res.Content[0])
	}
}

func TestCreateToolUnauthorized(t *testing.T) {
	t.Parallel()
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	tc := Context{
		Role:       "nobody",
		Dialect:    testdialect.Postgres{},
		Registry:   reg,
		Authorizer: rbac.NewRoleAuthorizer(reg),
	}
	in, _ := json.Marshal(createInput{Entity: "users", Values: map[string]any{"email": "x"}})
	_, err := CreateTool{}.Run(context.Background(), in, tc)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("got %v, want ErrUnauthorized", err)
	}
}

func TestUpdateToolWriteGuardRejectsNonPK(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name: "users", Source: "users",
		Attributes: []entity.Attribute{{Name: "id"}, {Name: "status"}},
		Keys:       []entity.Key{{Columns: []string{"id"}, Primary: true}},
		Role:       entity.RoleAccess{entity.ActionUpdate: {"writer"}},
		MCP:        entity.MCPFlags{DMLTools: true},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	auth := rbac.NewRoleAuthorizer(reg)
	db := &store.FakeDB{ExecFn: func(_ context.Context, _ string, _ ...any) (store.Result, error) {
		t.Fatal("non-PK write must not execute past the gate")
		return store.Result{}, nil
	}}
	gate := cost.NewGate(cost.StaticRule{PKWhitelist: true}, cost.WriteGuard{})
	tc := Context{Role: "writer", DB: db, Dialect: testdialect.Postgres{}, Registry: reg, Authorizer: auth, Gate: gate}
	in, _ := json.Marshal(updateInput{
		Entity: "users",
		Filter: []condJSON{{Field: "status", Op: "eq", Value: "active"}},
		Set:    map[string]any{"status": "x"},
	})
	_, err := UpdateTool{}.Run(context.Background(), in, tc)
	if !errors.Is(err, cost.ErrCostExceeded) {
		t.Fatalf("got %v, want ErrCostExceeded from WriteGuard", err)
	}
}

func TestCreateAuthorizesWithoutCostGate(t *testing.T) {
	t.Parallel()
	e := testUsersEntity()
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	events := []string{}
	db := &store.FakeDB{ExecFn: func(context.Context, string, ...any) (store.Result, error) {
		return store.Result{RowsAffected: 1}, nil
	}}
	tc := Context{
		Role: "writer", DB: db, Dialect: testdialect.MySQL{}, Registry: reg,
		Authorizer: recordingAuthorizer{events: &events, dec: rbac.Decision{Allowed: true}},
		Gate:       recordingGate{events: &events},
		Hooks: &hook.Hooks{OnAuthorize: func(context.Context, rbac.Request, rbac.Decision) {
			events = append(events, "authorize-hook")
		}},
	}
	in, _ := json.Marshal(createInput{Entity: "users", Values: map[string]any{"email": "a@x.com"}})
	if _, err := (CreateTool{}).Run(context.Background(), in, tc); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(events, ","); got != "authorize,authorize-hook" {
		t.Fatalf("create events = %s; cost gate must not run", got)
	}
}

func TestToolFieldACLByUsage(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name:       "users",
		Source:     "users",
		Attributes: []entity.Attribute{{Name: "id"}, {Name: "email"}},
		Keys:       []entity.Key{{Columns: []string{"id"}, Primary: true}},
		Role: entity.RoleAccess{
			entity.ActionRead:      {"editor"},
			entity.ActionCreate:    {"editor"},
			entity.ActionUpdate:    {"editor"},
			entity.ActionAggregate: {"editor"},
		},
		FieldAccess: entity.FieldAccess{
			"editor": {Read: []string{"id"}, Write: []string{"email"}},
		},
		MCP: entity.MCPFlags{DMLTools: true},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	tc := Context{
		Role: "editor", Dialect: testdialect.Postgres{}, Registry: reg,
		Authorizer: rbac.NewRoleAuthorizer(reg),
	}
	cases := []struct {
		name  string
		tool  Tool
		input string
	}{
		{"projection", ReadTool{}, `{"entity":"users","fields":["email"]}`},
		{"filter", ReadTool{}, `{"entity":"users","filter":[{"field":"email","op":"eq","value":"x"}]}`},
		{"create values", CreateTool{}, `{"entity":"users","values":{"id":1}}`},
		{"update set", UpdateTool{}, `{"entity":"users","filter":[{"field":"id","op":"eq","value":1}],"set":{"id":2}}`},
		{"group by", AggregateTool{}, `{"entity":"users","groupBy":["email"],"aggregates":[{"func":"count"}]}`},
	}
	for _, tcse := range cases {
		t.Run(tcse.name, func(t *testing.T) {
			_, err := tcse.tool.Run(context.Background(), json.RawMessage(tcse.input), tc)
			if !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("got %v, want ErrUnauthorized", err)
			}
		})
	}
}

func TestWritesRejectGeneratedColumns(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name: "orders", Source: "orders", MCP: entity.MCPFlags{DMLTools: true},
		Attributes: []entity.Attribute{
			{Name: "id", Domain: entity.Domain{AutoIncrement: true, Default: true}},
			{Name: "total"}, {Name: "total_cents", Domain: entity.Domain{Generated: true}},
		},
		Keys: []entity.Key{{Name: "pk", Columns: []string{"id"}, Primary: true}},
		Role: entity.RoleAccess{entity.ActionCreate: {"w"}, entity.ActionUpdate: {"w"}},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	executed := false
	db := &store.FakeDB{
		QueryFn: func(context.Context, string, ...any) (store.Rows, error) { executed = true; return nil, nil },
		ExecFn: func(context.Context, string, ...any) (store.Result, error) {
			executed = true
			return store.Result{}, nil
		},
	}
	tc := Context{
		Role: "w", DB: db, Dialect: testdialect.Postgres{}, Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg),
	}
	for tool, input := range map[Tool]string{
		CreateTool{}: `{"entity":"orders","values":{"total":1,"total_cents":100}}`,
		UpdateTool{}: `{"entity":"orders","filter":[{"field":"id","op":"eq","value":1}],"set":{"total_cents":100}}`,
	} {
		if _, err := tool.Run(context.Background(), json.RawMessage(input), tc); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: error = %v, want invalid input", tool.Info().Name, err)
		}
	}
	if executed {
		t.Fatal("a write to a generated column reached the database")
	}
}

func TestInsertWithoutReturningReportsTheRowKey(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name: "lines", Source: "lines", MCP: entity.MCPFlags{DMLTools: true},
		Attributes: []entity.Attribute{
			{Name: "tenant_id"}, {Name: "id", Domain: entity.Domain{AutoIncrement: true, Default: true}}, {Name: "sku"},
		},
		Keys: []entity.Key{{Name: "PRIMARY", Columns: []string{"tenant_id", "id"}, Primary: true}},
		Role: entity.RoleAccess{entity.ActionCreate: {"w"}},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	db := &store.FakeDB{ExecFn: func(context.Context, string, ...any) (store.Result, error) {
		return store.Result{LastInsertID: 42, RowsAffected: 1}, nil
	}}
	tc := Context{Role: "w", DB: db, Dialect: testdialect.MySQL{}, Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg)}
	input := json.RawMessage(`{"entity":"lines","values":{"tenant_id":7,"sku":"a"}}`)
	res, err := CreateTool{}.Run(context.Background(), input, tc)
	if err != nil {
		t.Fatal(err)
	}
	row := res.Content[0]
	if row["tenant_id"] != int64(7) || row["id"] != int64(42) || row["rowsAffected"] != int64(1) {
		t.Fatalf("row = %v, want the composite key of the inserted row", row)
	}
}
