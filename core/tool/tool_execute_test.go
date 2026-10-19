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
	"github.com/nethinwei/sql-mcp-server/core/store/storetest"
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
	db := &storetest.FakeDB{QueryFn: func(_ context.Context, q string, _ ...any) (store.Rows, error) {
		called = true
		if !strings.Contains(q, "CALL") {
			t.Errorf("expected CALL, got %q", q)
		}
		return storetest.NewFakeRows([]string{"n"}, []any{int64(42)}), nil
	}}
	events := []string{}
	tc := Context{
		Services: &Services{Registry: reg, Authorizer: auth},
		Role:     "caller",
		DB:       db,
		Dialect:  testdialect.Postgres{},
		Gate:     recordingGate{events: &events},
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
	tc := Context{Services: &Services{Registry: reg, Authorizer: auth}, Role: "reader", Dialect: testdialect.Postgres{}}
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
	_, err := ExecuteTool{}.Run(
		context.Background(),
		json.RawMessage(`{"entity":"sp","args":{"x":1}}`),
		Context{
			Services: &Services{Registry: registry, Authorizer: rbac.NewRoleAuthorizer(registry)},
			Role:     "caller",
			Dialect:  testdialect.Postgres{},
		},
	)
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
		Services: &Services{Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg)},
		Role:     "role",
		Dialect:  testdialect.Postgres{},
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
	db := &storetest.FakeDB{QueryFn: func(_ context.Context, _ string, args ...any) (store.Rows, error) {
		if len(args) != 1 || args[0] != "acme" {
			t.Fatalf("args = %v, want [acme]", args)
		}
		return storetest.NewFakeRows([]string{"ok"}, []any{true}), nil
	}}
	tc := Context{
		Services: &Services{Registry: reg, Authorizer: auth},
		Role:     "caller",
		DB:       db,
		Dialect:  testdialect.Postgres{},
	}
	pt := ProcedureTool{Entity: e}
	if pt.Info().Name != "procedure_refresh-cache" {
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
	db := &storetest.FakeDB{
		QueryFn: func(context.Context, string, ...any) (store.Rows, error) { return nil, want },
		ExecFn: func(context.Context, string, ...any) (store.Result, error) {
			t.Fatal("failed procedure query must not be executed a second time")
			return store.Result{}, nil
		},
	}
	tc := Context{
		Services: &Services{Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg)},
		Role:     "caller",
		DB:       db,
		Dialect:  testdialect.Postgres{},
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
	db := &storetest.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
		return storetest.NewFakeRows([]string{"visible", "secret", "excluded", "undeclared"}, []any{1, 2, 3, 4}), nil
	}}
	tc := Context{
		Services: &Services{Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg)},
		Role:     "caller",
		DB:       db,
		Dialect:  testdialect.Postgres{},
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
		Rows:   storetest.NewFakeRows([]string{"n"}, []any{int64(1)}),
		events: &events,
	}
	db := &storetest.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
		return rows, nil
	}}
	cc := &recordingCache{events: &events}
	tc := Context{
		Services: &Services{Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg), Cache: cc},
		Role:     "caller",
		DB:       db,
		Dialect:  testdialect.Postgres{},
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
		Rows:   storetest.NewFakeRows([]string{"n"}, []any{int64(1)}),
		events: &events,
	}
	db := &storetest.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
		return rows, nil
	}}
	want := errors.New("cache invalidation failed")
	tc := Context{
		Services: &Services{
			Registry:   reg,
			Authorizer: rbac.NewRoleAuthorizer(reg),
			Cache:      &recordingCache{events: &events, err: want},
		},
		Role:    "caller",
		DB:      db,
		Dialect: testdialect.Postgres{},
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
	rows             *storetest.FakeRows
	limits           budget.Limits
	maxProcedureRows int64
	want             error
} {
	return []struct {
		name             string
		rows             *storetest.FakeRows
		limits           budget.Limits
		maxProcedureRows int64
		want             error
	}{
		{
			name: "iteration error",
			rows: func() *storetest.FakeRows {
				rows := storetest.NewFakeRows([]string{"n"})
				rows.SetErr(errors.New("driver iteration failed"))
				return rows
			}(),
			want: ErrDatabase,
		},
		// Past its rows a procedure (which may have written) is cut, not
		// refused: a refusal would invite running it again.
		{
			name:   "budget rows cut",
			rows:   storetest.NewFakeRows([]string{"n"}, []any{1}, []any{2}),
			limits: budget.Limits{MaxReturnedRows: 1},
		},
		{
			name:             "procedure hard cap cut",
			rows:             storetest.NewFakeRows([]string{"n"}, []any{1}, []any{2}),
			maxProcedureRows: 1,
		},
	}
}

func assertExecuteConsumptionError(
	t *testing.T,
	reg *entity.Registry,
	tcse struct {
		name             string
		rows             *storetest.FakeRows
		limits           budget.Limits
		maxProcedureRows int64
		want             error
	},
) {
	t.Helper()
	cc := &recordingCache{}
	db := &storetest.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
		return tcse.rows, nil
	}}
	tc := Context{
		Services: &Services{
			Registry:   reg,
			Authorizer: rbac.NewRoleAuthorizer(reg),
			Cache:      cc,
			Limits:     Limits{MaxProcedureRows: tcse.maxProcedureRows},
		},
		Role:         "caller",
		DB:           db,
		Dialect:      testdialect.Postgres{},
		BudgetLimits: tcse.limits,
	}
	res, err := (ExecuteTool{}).Run(context.Background(), json.RawMessage(`{"entity":"sp","args":{"x":1}}`), tc)
	if tcse.want == nil && (err != nil || !res.Truncated || len(res.Content) != 1) {
		t.Fatalf("result = %d rows, truncated %v, error %v; want the first row, truncated",
			len(res.Content), res.Truncated, err)
	}
	if !errors.Is(err, tcse.want) {
		t.Fatalf("error = %v, want %v", err, tcse.want)
	}
	if !tcse.rows.Closed() {
		t.Fatal("rows were not closed")
	}
	// The procedure ran and may have written, so the cache is invalidated
	// even though its result could not be returned.
	if cc.invalidations != 1 {
		t.Fatalf("invalidations = %d, want 1", cc.invalidations)
	}
}

func TestProcedureToolSchemaRequiresEmptyListWithoutParams(t *testing.T) {
	t.Parallel()
	info := ProcedureTool{Entity: entity.Entity{Name: "ping", Kind: entity.KindProcedure}}.Info()
	var schema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(info.InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Required == nil || len(schema.Required) != 0 {
		t.Fatalf("required = %v (%s), want []", schema.Required, info.InputSchema)
	}
}

// Procedure tools are named after the procedure; only procedures whose names
// collide tell themselves apart by a hash of their full name.
func TestProcedureToolNames(t *testing.T) {
	t.Parallel()
	procedure := func(name, local string) entity.Entity {
		return entity.Entity{Name: name, Local: local, Kind: entity.KindProcedure,
			MCP: entity.MCPFlags{CustomTool: true, TrustedProcedure: true}}
	}
	reg, err := entity.NewRegistry([]entity.Entity{
		procedure("shop.close_day", "close_day"), procedure("ledger.close_day", "close_day"),
		procedure("shop.refresh", "refresh"), procedure("shop.post entry", "post entry"),
	})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, pt := range ProcedureTools(reg) {
		names[pt.Entity.Name] = pt.Info().Name
		if found, ok := FindProcedureTool(reg, pt.Info().Name); !ok || found.Entity.Name != pt.Entity.Name {
			t.Fatalf("%s does not find %s", pt.Info().Name, pt.Entity.Name)
		}
	}
	if names["shop.refresh"] != "procedure_refresh" || names["shop.post entry"] != "procedure_post_entry" {
		t.Fatalf("readable names = %v", names)
	}
	shop, ledger := names["shop.close_day"], names["ledger.close_day"]
	if shop == ledger || !strings.HasPrefix(shop, "procedure_close_day_") || len(shop) != len("procedure_close_day_")+8 {
		t.Fatalf("colliding names = %q, %q", shop, ledger)
	}
	if _, ok := FindProcedureTool(reg, "procedure_close_day"); ok {
		t.Fatal("an ambiguous base name must not find a procedure")
	}
}
