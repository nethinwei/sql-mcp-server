package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/budget"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/internal/testdialect"
)

func TestExecuteToolCallsProcedure(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name: "sp", Source: "sp", Kind: entity.KindProcedure,
		Role:       entity.RoleAccess{entity.ActionExecute: {"caller"}},
		MCP:        entity.MCPFlags{DMLTools: true, TrustedProcedure: true},
		Params:     []string{"x"},
		Attributes: []entity.Attribute{{Name: "n"}},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	auth := rbac.NewRoleAuthorizer(reg)
	called := false
	db := &store.FakeDB{QueryFn: func(_ context.Context, q string, _ ...any) (store.Rows, error) {
		called = true
		if !strings.Contains(q, "CALL") {
			t.Errorf("expected CALL, got %q", q)
		}
		return store.NewFakeRows([]string{"n"}, []any{int64(42)}), nil
	}}
	events := []string{}
	tc := Context{
		Role: "caller", DB: db, Dialect: testdialect.Postgres{}, Registry: reg,
		Authorizer: auth, Gate: recordingGate{events: &events},
	}
	in, _ := json.Marshal(executeInput{Entity: "sp", Args: map[string]any{"x": 1}})
	res, err := ExecuteTool{}.Run(context.Background(), in, tc)
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("procedure not called")
	}
	if len(events) != 1 || events[0] != "gate" {
		t.Fatalf("execute cost gate events = %v", events)
	}
	if len(res.Content) != 1 || res.Content[0]["n"] != int64(42) {
		t.Fatalf("got %v", res.Content)
	}
}

func TestExecuteToolRejectsNonProcedure(t *testing.T) {
	t.Parallel()
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	auth := rbac.NewRoleAuthorizer(reg)
	tc := Context{Role: "reader", Dialect: testdialect.Postgres{}, Registry: reg, Authorizer: auth}
	in, _ := json.Marshal(executeInput{Entity: "users"})
	_, err := ExecuteTool{}.Run(context.Background(), in, tc)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("got %v, want ErrInvalidInput", err)
	}
}

func TestExecuteToolRejectsUntrustedProcedure(t *testing.T) {
	entityConfig := entity.Entity{
		Name: "sp", Source: "sp", Kind: entity.KindProcedure, Params: []string{"x"},
		Role: entity.RoleAccess{entity.ActionExecute: {"caller"}},
		MCP:  entity.MCPFlags{DMLTools: true},
	}
	registry, _ := entity.NewRegistry([]entity.Entity{entityConfig})
	_, err := ExecuteTool{}.Run(context.Background(), json.RawMessage(`{"entity":"sp","args":{"x":1}}`), Context{
		Role: "caller", Dialect: testdialect.Postgres{}, Registry: registry,
		Authorizer: rbac.NewRoleAuthorizer(registry),
	})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("error = %v, want unauthorized", err)
	}
}

func TestEntityToolsRejectDMLDisabled(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name:       "hidden",
		Source:     "hidden",
		Kind:       entity.KindProcedure,
		Attributes: []entity.Attribute{{Name: "id"}},
		Params:     []string{"id"},
		Role: entity.RoleAccess{
			entity.ActionRead: {"role"}, entity.ActionCreate: {"role"},
			entity.ActionUpdate: {"role"}, entity.ActionDelete: {"role"},
			entity.ActionExecute: {"role"}, entity.ActionAggregate: {"role"},
		},
		MCP: entity.MCPFlags{DMLTools: false},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	tc := Context{
		Role: "role", Dialect: testdialect.Postgres{}, Registry: reg,
		Authorizer: rbac.NewRoleAuthorizer(reg),
	}
	cases := []struct {
		name  string
		tool  Tool
		input string
	}{
		{"describe", DescribeTool{}, `{"entity":"hidden"}`},
		{"read", ReadTool{}, `{"entity":"hidden"}`},
		{"create", CreateTool{}, `{"entity":"hidden","values":{"id":1}}`},
		{"update", UpdateTool{}, `{"entity":"hidden","filter":[],"set":{"id":1}}`},
		{"delete", DeleteTool{}, `{"entity":"hidden","filter":[]}`},
		{"execute", ExecuteTool{}, `{"entity":"hidden","args":{"id":1}}`},
		{"aggregate", AggregateTool{}, `{"entity":"hidden","aggregates":[]}`},
	}
	for _, tcse := range cases {
		t.Run(tcse.name, func(t *testing.T) {
			_, err := tcse.tool.Run(context.Background(), json.RawMessage(tcse.input), tc)
			if !errors.Is(err, ErrDMLToolsDisabled) {
				t.Fatalf("got %v, want ErrDMLToolsDisabled", err)
			}
		})
	}
}

func TestProcedureToolRunsWhenDMLDisabled(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name: "refresh-cache", Source: "refresh_cache", Kind: entity.KindProcedure,
		Params:     []string{"tenant"},
		Role:       entity.RoleAccess{entity.ActionExecute: {"caller"}},
		MCP:        entity.MCPFlags{CustomTool: true, TrustedProcedure: true},
		Attributes: []entity.Attribute{{Name: "ok"}},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	auth := rbac.NewRoleAuthorizer(reg)
	db := &store.FakeDB{QueryFn: func(_ context.Context, _ string, args ...any) (store.Rows, error) {
		if len(args) != 1 || args[0] != "acme" {
			t.Fatalf("args = %v, want [acme]", args)
		}
		return store.NewFakeRows([]string{"ok"}, []any{true}), nil
	}}
	tc := Context{
		Role: "caller", DB: db, Dialect: testdialect.Postgres{},
		Registry: reg, Authorizer: auth,
	}
	pt := ProcedureTool{Entity: e}
	if pt.Info().Name != ProcedureToolName(e.Name) || strings.Contains(pt.Info().Name, "execute_entity") {
		t.Fatalf("unstable or conflicting tool name %q", pt.Info().Name)
	}
	res, err := pt.Run(context.Background(), json.RawMessage(`{"tenant":"acme"}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Content) != 1 || res.Content[0]["ok"] != true {
		t.Fatalf("got %v", res.Content)
	}
}

func TestExecuteToolDoesNotRetryQueryFailureWithExec(t *testing.T) {
	t.Parallel()
	want := errors.New("provider query failed")
	e := entity.Entity{
		Name: "sp", Source: "sp", Kind: entity.KindProcedure, Params: []string{"x"},
		Role: entity.RoleAccess{
			entity.ActionExecute: {"caller"},
		}, MCP: entity.MCPFlags{DMLTools: true, TrustedProcedure: true},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	db := &store.FakeDB{
		QueryFn: func(context.Context, string, ...any) (store.Rows, error) { return nil, want },
		ExecFn: func(context.Context, string, ...any) (store.Result, error) {
			t.Fatal("failed procedure query must not be executed a second time")
			return store.Result{}, nil
		},
	}
	tc := Context{
		Role:       "caller",
		DB:         db,
		Dialect:    testdialect.Postgres{},
		Registry:   reg,
		Authorizer: rbac.NewRoleAuthorizer(reg),
	}
	_, err := ExecuteTool{}.Run(context.Background(), json.RawMessage(`{"entity":"sp","args":{"x":1}}`), tc)
	if !errors.Is(err, ErrDatabase) || !errors.Is(err, want) || len(db.Execs) != 0 {
		t.Fatalf("error = %v, execs = %d", err, len(db.Execs))
	}
}

func TestExecuteToolFiltersProcedureColumnsFailClosed(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name: "sp", Source: "sp", Kind: entity.KindProcedure, Params: []string{"x"},
		Attributes:  []entity.Attribute{{Name: "visible"}, {Name: "secret"}, {Name: "excluded", Excluded: true}},
		Role:        entity.RoleAccess{entity.ActionExecute: {"caller"}},
		FieldAccess: entity.FieldAccess{"caller": {Read: []string{"visible"}}},
		MCP:         entity.MCPFlags{DMLTools: true, TrustedProcedure: true},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	db := &store.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
		return store.NewFakeRows([]string{"visible", "secret", "excluded", "undeclared"}, []any{1, 2, 3, 4}), nil
	}}
	tc := Context{
		Role:       "caller",
		DB:         db,
		Dialect:    testdialect.Postgres{},
		Registry:   reg,
		Authorizer: rbac.NewRoleAuthorizer(reg),
	}
	result, err := ExecuteTool{}.Run(context.Background(), json.RawMessage(`{"entity":"sp","args":{"x":1}}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 || len(result.Content[0]) != 1 || result.Content[0]["visible"] != 1 {
		t.Fatalf("procedure output leaked fields: %#v", result.Content)
	}
}

func TestExecuteToolClosesRowsBeforeAfterWrite(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name: "sp", Source: "sp", Kind: entity.KindProcedure, Params: []string{"x"},
		Attributes: []entity.Attribute{{Name: "n"}},
		Role:       entity.RoleAccess{entity.ActionExecute: {"caller"}},
		MCP:        entity.MCPFlags{DMLTools: true, TrustedProcedure: true},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	events := []string{}
	rows := &recordingRows{
		Rows:   store.NewFakeRows([]string{"n"}, []any{int64(1)}),
		events: &events,
	}
	db := &store.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
		return rows, nil
	}}
	cc := &recordingCache{events: &events}
	tc := Context{
		Role: "caller", DB: db, Dialect: testdialect.Postgres{}, Registry: reg,
		Authorizer: rbac.NewRoleAuthorizer(reg), Cache: cc,
	}
	_, err := (ExecuteTool{}).Run(context.Background(), json.RawMessage(`{"entity":"sp","args":{"x":1}}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(events, ","); got != "close,invalidate" {
		t.Fatalf("event order = %s, want close,invalidate", got)
	}
}

func TestExecuteToolClosesRowsOnAfterWriteError(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name: "sp", Source: "sp", Kind: entity.KindProcedure, Params: []string{"x"},
		Attributes: []entity.Attribute{{Name: "n"}},
		Role:       entity.RoleAccess{entity.ActionExecute: {"caller"}},
		MCP:        entity.MCPFlags{DMLTools: true, TrustedProcedure: true},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	events := []string{}
	rows := &recordingRows{
		Rows:   store.NewFakeRows([]string{"n"}, []any{int64(1)}),
		events: &events,
	}
	db := &store.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
		return rows, nil
	}}
	want := errors.New("cache invalidation failed")
	tc := Context{
		Role: "caller", DB: db, Dialect: testdialect.Postgres{}, Registry: reg,
		Authorizer: rbac.NewRoleAuthorizer(reg),
		Cache:      &recordingCache{events: &events, err: want},
	}
	_, err := (ExecuteTool{}).Run(context.Background(), json.RawMessage(`{"entity":"sp","args":{"x":1}}`), tc)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if got := strings.Join(events, ","); got != "close,invalidate" {
		t.Fatalf("event order = %s, want close,invalidate", got)
	}
}

func TestExecuteToolClosesRowsOnConsumptionErrors(t *testing.T) {
	t.Parallel()
	reg := procedureConsumptionRegistry(t)
	for _, tcse := range procedureConsumptionCases() {
		t.Run(tcse.name, func(t *testing.T) {
			assertExecuteConsumptionError(t, reg, tcse)
		})
	}
}

func procedureConsumptionRegistry(t *testing.T) *entity.Registry {
	t.Helper()
	e := entity.Entity{
		Name: "sp", Source: "sp", Kind: entity.KindProcedure, Params: []string{"x"},
		Attributes: []entity.Attribute{{Name: "n"}},
		Role:       entity.RoleAccess{entity.ActionExecute: {"caller"}},
		MCP:        entity.MCPFlags{DMLTools: true, TrustedProcedure: true},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	return reg
}

func procedureConsumptionCases() []struct {
	name             string
	rows             *store.FakeRows
	limits           budget.Limits
	maxProcedureRows int64
	want             error
} {
	return []struct {
		name             string
		rows             *store.FakeRows
		limits           budget.Limits
		maxProcedureRows int64
		want             error
	}{
		{
			name: "iteration error",
			rows: func() *store.FakeRows {
				rows := store.NewFakeRows([]string{"n"})
				rows.SetErr(errors.New("driver iteration failed"))
				return rows
			}(),
			want: ErrDatabase,
		},
		{
			name:   "budget error",
			rows:   store.NewFakeRows([]string{"n"}, []any{1}, []any{2}),
			limits: budget.Limits{MaxReturnedRows: 1},
			want:   budget.ErrExceeded,
		},
		{
			name:             "procedure hard cap",
			rows:             store.NewFakeRows([]string{"n"}, []any{1}, []any{2}),
			maxProcedureRows: 1,
			want:             budget.ErrExceeded,
		},
	}
}

func assertExecuteConsumptionError(
	t *testing.T,
	reg *entity.Registry,
	tcse struct {
		name             string
		rows             *store.FakeRows
		limits           budget.Limits
		maxProcedureRows int64
		want             error
	},
) {
	t.Helper()
	cc := &recordingCache{}
	db := &store.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
		return tcse.rows, nil
	}}
	tc := Context{
		Role: "caller", DB: db, Dialect: testdialect.Postgres{}, Registry: reg,
		Authorizer: rbac.NewRoleAuthorizer(reg), Cache: cc, BudgetLimits: tcse.limits,
		MaxProcedureRows: tcse.maxProcedureRows,
	}
	_, err := (ExecuteTool{}).Run(context.Background(), json.RawMessage(`{"entity":"sp","args":{"x":1}}`), tc)
	if !errors.Is(err, tcse.want) {
		t.Fatalf("error = %v, want %v", err, tcse.want)
	}
	if !tcse.rows.Closed() {
		t.Fatal("rows were not closed")
	}
	if cc.invalidations != 0 {
		t.Fatal("afterWrite ran before successful result consumption")
	}
}
