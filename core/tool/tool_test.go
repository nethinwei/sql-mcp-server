package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/audit"
	"github.com/nethinwei/sql-mcp-server/core/budget"
	"github.com/nethinwei/sql-mcp-server/core/cache"
	"github.com/nethinwei/sql-mcp-server/core/codegen"
	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/engine"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/hook"
	"github.com/nethinwei/sql-mcp-server/core/mask"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/internal/testdialect"
)

// recorderAuditor is a synchronous audit.Auditor fake for tests.
type recorderAuditor struct{ events []audit.Event }

func (r *recorderAuditor) Record(_ context.Context, e audit.Event) error {
	r.events = append(r.events, e)
	return nil
}

type queryTx struct {
	store.FakeTx
	query func(context.Context, string, ...any) (store.Rows, error)
}

type recordingRows struct {
	store.Rows
	events *[]string
	once   sync.Once
}

func (r *recordingRows) Close() error {
	var err error
	r.once.Do(func() {
		*r.events = append(*r.events, "close")
		err = r.Rows.Close()
	})
	return err
}

type recordingCache struct {
	events        *[]string
	invalidations int
	err           error
}

func (*recordingCache) Get(context.Context, cache.Key) ([]map[string]any, bool) {
	return nil, false
}
func (*recordingCache) Set(context.Context, cache.Key, []map[string]any) error { return nil }
func (c *recordingCache) Invalidate(string) error {
	c.invalidations++
	if c.events != nil {
		*c.events = append(*c.events, "invalidate")
	}
	return c.err
}

func (t *queryTx) QueryContext(ctx context.Context, query string, args ...any) (store.Rows, error) {
	return t.query(ctx, query, args...)
}

type recordingAuthorizer struct {
	events *[]string
	dec    rbac.Decision
}

func (a recordingAuthorizer) Authorize(_ context.Context, _ rbac.Request) (rbac.Decision, error) {
	*a.events = append(*a.events, "authorize")
	return a.dec, nil
}

type recordingGate struct {
	events *[]string
}

func (g recordingGate) Check(_ context.Context, _ codegen.Compiled) (cost.Decision, error) {
	*g.events = append(*g.events, "gate")
	return cost.Decision{Allow: true}, nil
}

type recordingAnalyzeSampler struct {
	calls []codegen.Compiled
	plan  cost.Plan
}

type blockingTransactionReadTool struct {
	started chan struct{}
	release chan struct{}
}

func (blockingTransactionReadTool) Info() Info {
	return Info{Name: "transaction_read_probe", ReadOnly: true}
}
func (blockingTransactionReadTool) Enabled(config.ToolFlags) bool { return true }
func (t blockingTransactionReadTool) Run(context.Context, json.RawMessage, Context) (Result, error) {
	close(t.started)
	<-t.release
	return Result{Content: []map[string]any{{"ok": true}}}, nil
}

func (s *recordingAnalyzeSampler) ExplainAnalyze(_ context.Context, compiled codegen.Compiled) (cost.Plan, error) {
	s.calls = append(s.calls, compiled)
	return s.plan, nil
}

func testUsersEntity() entity.Entity {
	return entity.Entity{
		Name:       "users",
		Source:     "users",
		Attributes: []entity.Attribute{{Name: "id"}, {Name: "email", Mask: "email"}},
		Keys:       []entity.Key{{Columns: []string{"id"}, Primary: true}},
		Role:       entity.RoleAccess{entity.ActionRead: {"reader"}, entity.ActionCreate: {"writer"}},
		MCP:        entity.MCPFlags{DMLTools: true},
	}
}

func TestRegistryEnabledFilters(t *testing.T) {
	t.Parallel()
	r, _ := NewRegistry(DefaultTools())
	enabled := r.Enabled(config.DefaultToolFlags()) // delete off
	if len(enabled) != 9 {
		t.Fatalf("got %d tools, want 9 (delete off, transaction tools on)", len(enabled))
	}
	for _, tl := range enabled {
		if tl.Info().Name == "delete_record" {
			t.Fatal("delete_record should be filtered out")
		}
	}
	all := r.Enabled(config.ToolFlags{
		DescribeEntities: true, ReadRecords: true, CreateRecord: true,
		UpdateRecord: true, DeleteRecord: true, ExecuteEntity: true, AggregateRecords: true,
	})
	if len(all) != 7 {
		t.Fatalf("got %d, want 7", len(all))
	}
}

func TestReadToolEndToEnd(t *testing.T) {
	t.Parallel()
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	auth := rbac.NewRoleAuthorizer(reg)
	db := &store.FakeDB{QueryFn: func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
		return store.NewFakeRows([]string{"id", "email"}, []any{int64(1), "alice@x.com"}), nil
	}}
	tc := Context{
		Role: "reader", DB: db, Dialect: testdialect.Postgres{},
		Registry: reg, Authorizer: auth, Masker: mask.NewRuleMasker(nil),
	}
	in, _ := json.Marshal(readInput{Entity: "users", Filter: []condJSON{{Field: "id", Op: "eq", Value: int64(1)}}})
	res, err := ReadTool{}.Run(context.Background(), in, tc)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("rows = %v", res.Content)
	}
	if res.Content[0]["email"] != "a***@x.com" {
		t.Fatalf("email not masked: %v", res.Content[0]["email"])
	}
}

func TestReadToolRecordsExplainAnalyzeFeedback(t *testing.T) {
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	db := &store.FakeDB{QueryFn: func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
		return store.NewFakeRows([]string{"id", "email"}, []any{int64(1), "a@x.com"}), nil
	}}
	feedback := cost.NewMemoryStore()
	tc := Context{
		Role: "reader", DB: db, Dialect: testdialect.Postgres{}, Registry: reg,
		Authorizer: rbac.NewRoleAuthorizer(reg), Feedback: feedback,
		Analyze: cost.AnalyzePolicy{
			Sampler: cost.FakeAnalyzeSampler{Plan: cost.Plan{ActualRows: 25, ExecutionTime: 3 * time.Millisecond}},
			Config:  cost.AnalyzeConfig{Enabled: true, ReadOnly: true, SampleRate: 1, Timeout: time.Second},
		},
	}
	input, _ := json.Marshal(readInput{Entity: "users"})
	if _, err := (ReadTool{}).Run(context.Background(), input, tc); err != nil {
		t.Fatal(err)
	}
	query := db.Queries[0]
	fingerprint := cost.Fingerprint("default", "postgres", codegen.Compiled{
		SQL: query.Query, Args: query.Args, ReadOnly: true,
	})
	stats, ok := feedback.Stats(fingerprint)
	if !ok || stats.LatestRows != 25 || stats.LatestTime != 3*time.Millisecond {
		t.Fatalf("feedback = %+v, ok = %v", stats, ok)
	}
}

func TestReadToolIgnoresExplainAnalyzeFailure(t *testing.T) {
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	db := &store.FakeDB{QueryFn: func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
		return store.NewFakeRows([]string{"id", "email"}, []any{int64(1), "a@x.com"}), nil
	}}
	auditor := &recorderAuditor{}
	var hookErr error
	tc := Context{
		Role: "reader", DB: db, Dialect: testdialect.Postgres{}, Registry: reg,
		Authorizer: rbac.NewRoleAuthorizer(reg), Feedback: cost.NewMemoryStore(), Auditor: auditor,
		Hooks: &hook.Hooks{OnError: func(_ context.Context, err error) { hookErr = err }},
		Analyze: cost.AnalyzePolicy{
			Sampler: cost.FakeAnalyzeSampler{Err: errors.New("sampling timeout")},
			Config:  cost.AnalyzeConfig{Enabled: true, ReadOnly: true, SampleRate: 1, Timeout: time.Second},
		},
	}
	input, _ := json.Marshal(readInput{Entity: "users"})
	result, err := (ReadTool{}).Run(context.Background(), input, tc)
	if err != nil || len(result.Content) != 1 {
		t.Fatalf("successful read changed by sampling failure: result=%+v err=%v", result, err)
	}
	if hookErr == nil || len(auditor.events) != 1 || auditor.events[0].Action != "explain_analyze_sample" {
		t.Fatalf("sampling failure observability: hook=%v events=%+v", hookErr, auditor.events)
	}
}

func TestReadToolSelectsAnalyzeSamplerByEntityDatasource(t *testing.T) {
	mainEntity := testUsersEntity()
	mainEntity.DataSource = "main"
	archiveEntity := testUsersEntity()
	archiveEntity.Name = "archived_users"
	archiveEntity.DataSource = "archive"
	reg, _ := entity.NewRegistry([]entity.Entity{mainEntity, archiveEntity})
	mainDB := &store.FakeDB{}
	archiveDB := &store.FakeDB{QueryFn: func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
		return store.NewFakeRows([]string{"id"}, []any{int64(1)}), nil
	}}
	mainSampler := &recordingAnalyzeSampler{}
	archiveSampler := &recordingAnalyzeSampler{plan: cost.Plan{ActualRows: 7}}
	policy := func(s cost.AnalyzeSampler) cost.AnalyzePolicy {
		return cost.AnalyzePolicy{
			Sampler: s,
			Config:  cost.AnalyzeConfig{Enabled: true, ReadOnly: true, SampleRate: 1, Timeout: time.Second},
		}
	}
	tc := Context{
		Role: "reader", Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg),
		Feedback: cost.NewMemoryStore(),
		Sources: map[string]DataSource{
			"main":    {DB: mainDB, Dialect: testdialect.Postgres{}, Analyze: policy(mainSampler)},
			"archive": {DB: archiveDB, Dialect: testdialect.Postgres{}, Analyze: policy(archiveSampler)},
		},
	}
	input, _ := json.Marshal(readInput{Entity: "archived_users"})
	if _, err := (ReadTool{}).Run(context.Background(), input, tc); err != nil {
		t.Fatal(err)
	}
	if len(mainSampler.calls) != 0 || len(archiveSampler.calls) != 1 {
		t.Fatalf("sampler calls = main:%d archive:%d", len(mainSampler.calls), len(archiveSampler.calls))
	}
}

func TestReadToolBatchExpandsRelationship(t *testing.T) {
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
	db := &store.FakeDB{}
	queries := 0
	db.QueryFn = func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
		queries++
		if queries == 1 {
			return store.NewFakeRows([]string{"id", "email"}, []any{int64(1), "a@x.com"}, []any{int64(2), "b@x.com"}), nil
		}
		return store.NewFakeRows([]string{"id", "user_id"}, []any{int64(10), int64(1)}, []any{int64(11), int64(1)}), nil
	}
	auth := rbac.NewRoleAuthorizer(reg)
	tc := Context{
		Role: "reader", DB: db, Dialect: testdialect.Postgres{}, Registry: reg, Authorizer: auth,
		Sources: map[string]DataSource{"default": {DB: db, Dialect: testdialect.Postgres{}}},
	}
	input, _ := json.Marshal(readInput{Entity: "users", Expand: []string{"orders"}})
	result, err := ReadTool{}.Run(context.Background(), input, tc)
	if err != nil {
		t.Fatal(err)
	}
	if queries != 2 {
		t.Fatalf("queries = %d, want one parent and one batch expansion", queries)
	}
	children, ok := result.Content[0]["orders"].([]map[string]any)
	if !ok || len(children) != 2 {
		t.Fatalf("expanded orders = %#v", result.Content[0]["orders"])
	}
	queries = 0
	tc.BudgetLimits.MaxReturnedRows = 3
	if _, err := (ReadTool{}).Run(context.Background(), input, tc); !errors.Is(err, budget.ErrExceeded) {
		t.Fatalf("parent + expand row limit error = %v", err)
	}
}

func TestReadToolCostReject(t *testing.T) {
	t.Parallel()
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	auth := rbac.NewRoleAuthorizer(reg)
	db := &store.FakeDB{QueryFn: func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
		t.Fatal("query should not execute when gate rejects")
		return nil, nil
	}}
	gate := cost.NewGate(cost.Estimate{
		Explainer: cost.FakeExplainer{Plan: cost.Plan{ScanType: cost.ScanFull, StatsFresh: true}},
		Threshold: cost.Threshold{RejectFullScan: true},
	})
	tc := Context{Role: "reader", DB: db, Dialect: testdialect.Postgres{}, Registry: reg, Authorizer: auth, Gate: gate}
	in, _ := json.Marshal(readInput{Entity: "users"})
	_, err := ReadTool{}.Run(context.Background(), in, tc)
	if !errors.Is(err, cost.ErrCostExceeded) {
		t.Fatalf("got %v, want ErrCostExceeded", err)
	}
}

func TestReadToolUnauthorized(t *testing.T) {
	t.Parallel()
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	auth := rbac.NewRoleAuthorizer(reg)
	tc := Context{Role: "nobody", Dialect: testdialect.Postgres{}, Registry: reg, Authorizer: auth}
	in, _ := json.Marshal(readInput{Entity: "users"})
	_, err := ReadTool{}.Run(context.Background(), in, tc)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("got %v, want ErrUnauthorized", err)
	}
}

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
	if _, err := (DescribeTool{}).Run(context.Background(), json.RawMessage(`{"entity":"admin_only"}`), tc); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unauthorized detail error = %v", err)
	}
}

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
		Role: entity.RoleAccess{entity.ActionExecute: {"caller"}}, MCP: entity.MCPFlags{DMLTools: true, TrustedProcedure: true},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	db := &store.FakeDB{
		QueryFn: func(context.Context, string, ...any) (store.Rows, error) { return nil, want },
		ExecFn: func(context.Context, string, ...any) (store.Result, error) {
			t.Fatal("failed procedure query must not be executed a second time")
			return store.Result{}, nil
		},
	}
	tc := Context{Role: "caller", DB: db, Dialect: testdialect.Postgres{}, Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg)}
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
	tc := Context{Role: "caller", DB: db, Dialect: testdialect.Postgres{}, Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg)}
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
	e := entity.Entity{
		Name: "sp", Source: "sp", Kind: entity.KindProcedure, Params: []string{"x"},
		Attributes: []entity.Attribute{{Name: "n"}},
		Role:       entity.RoleAccess{entity.ActionExecute: {"caller"}},
		MCP:        entity.MCPFlags{DMLTools: true, TrustedProcedure: true},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	cases := []struct {
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
	for _, tcse := range cases {
		t.Run(tcse.name, func(t *testing.T) {
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
		})
	}
}

func TestWrapDBErrorPreservesCause(t *testing.T) {
	t.Parallel()
	cause := errors.New("driver failed")
	err := WrapDBError(cause)
	if !errors.Is(err, ErrDatabase) || !errors.Is(err, cause) || errors.Unwrap(err) != cause {
		t.Fatalf("wrapped error = %v", err)
	}
	if WrapDBError(nil) != nil || WrapDBError(err) != err {
		t.Fatal("WrapDBError should preserve nil and existing wrappers")
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

func TestAggregateTool(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name: "users", Source: "users",
		Attributes: []entity.Attribute{{Name: "dept"}, {Name: "salary"}},
		Keys:       []entity.Key{{Columns: []string{"id"}, Primary: true}},
		Role:       entity.RoleAccess{entity.ActionAggregate: {"reader"}},
		MCP:        entity.MCPFlags{DMLTools: true},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	auth := rbac.NewRoleAuthorizer(reg)
	db := &store.FakeDB{QueryFn: func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
		return store.NewFakeRows([]string{"dept", "count"}, []any{"eng", int64(5)}), nil
	}}
	tc := Context{Role: "reader", DB: db, Dialect: testdialect.Postgres{}, Registry: reg, Authorizer: auth}
	in, _ := json.Marshal(aggregateInput{Entity: "users", GroupBy: []string{"dept"}, Aggregates: []aggJSON{{Func: "count"}}})
	res, err := AggregateTool{}.Run(context.Background(), in, tc)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("got %v", res.Content)
	}
}

func TestAggregateToolRejectsInvalidFunc(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name: "users", Source: "users",
		Attributes: []entity.Attribute{{Name: "dept"}},
		Role:       entity.RoleAccess{entity.ActionAggregate: {"reader"}},
		MCP:        entity.MCPFlags{DMLTools: true},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	auth := rbac.NewRoleAuthorizer(reg)
	db := &store.FakeDB{QueryFn: func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
		t.Fatal("query must not run for an invalid aggregate func")
		return nil, nil
	}}
	tc := Context{Role: "reader", DB: db, Dialect: testdialect.Postgres{}, Registry: reg, Authorizer: auth}
	in, _ := json.Marshal(aggregateInput{Entity: "users", Aggregates: []aggJSON{{Func: "count(*);drop"}}})
	_, err := AggregateTool{}.Run(context.Background(), in, tc)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("got %v, want ErrInvalidInput", err)
	}
}

func TestAggregateToolMasksResultRows(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name:       "users",
		Source:     "users",
		Attributes: []entity.Attribute{{Name: "email", Mask: "email"}},
		Role:       entity.RoleAccess{entity.ActionAggregate: {"reader"}},
		MCP:        entity.MCPFlags{DMLTools: true},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	db := &store.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
		return store.NewFakeRows([]string{"email"}, []any{"alice@example.com"}), nil
	}}
	tc := Context{
		Role: "reader", DB: db, Dialect: testdialect.Postgres{}, Registry: reg,
		Authorizer: rbac.NewRoleAuthorizer(reg), Masker: mask.NewRuleMasker(nil),
	}
	result, err := (AggregateTool{}).Run(context.Background(), json.RawMessage(`{"entity":"users","aggregates":[{"func":"count"}]}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Content[0]["email"]; got != "a***@example.com" {
		t.Fatalf("aggregate result was not masked: %v", got)
	}
}

func TestMaskedFieldsRejectedForValueRevealingUsages(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name:       "users",
		Source:     "users",
		Attributes: []entity.Attribute{{Name: "id"}, {Name: "secret", Alias: "private", Mask: "email"}},
		Keys:       []entity.Key{{Columns: []string{"id"}, Primary: true}},
		Role: entity.RoleAccess{
			entity.ActionRead:      {"role"},
			entity.ActionCreate:    {"role"},
			entity.ActionUpdate:    {"role"},
			entity.ActionDelete:    {"role"},
			entity.ActionAggregate: {"role"},
		},
		MCP: entity.MCPFlags{DMLTools: true},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	db := &store.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
		return store.NewFakeRows([]string{"secret"}, []any{"alice@example.com"}), nil
	}}
	tc := Context{
		Role: "role", DB: db, Dialect: testdialect.Postgres{}, Registry: reg,
		Authorizer: rbac.NewRoleAuthorizer(reg),
	}
	projection, err := (ReadTool{}).Run(context.Background(), json.RawMessage(`{"entity":"users","fields":["secret"]}`), tc)
	if err != nil || len(projection.Content) != 1 {
		t.Fatalf("read projection should allow masked field: result=%v error=%v", projection.Content, err)
	}
	cases := []struct {
		name  string
		tool  Tool
		input string
	}{
		{"read filter", ReadTool{}, `{"entity":"users","filter":[{"field":"secret","op":"eq","value":"x"}]}`},
		{"read cursor alias", ReadTool{}, `{"entity":"users","cursor":{"private":"x"}}`},
		{"update filter", UpdateTool{}, `{"entity":"users","filter":[{"field":"secret","op":"eq","value":"x"}],"set":{"id":1}}`},
		{"delete filter", DeleteTool{}, `{"entity":"users","filter":[{"field":"secret","op":"eq","value":"x"}]}`},
		{"group by", AggregateTool{}, `{"entity":"users","groupBy":["secret"],"aggregates":[{"func":"count"}]}`},
		{"aggregate field", AggregateTool{}, `{"entity":"users","aggregates":[{"func":"max","field":"secret"}]}`},
	}
	for _, tcse := range cases {
		t.Run(tcse.name, func(t *testing.T) {
			_, err := tcse.tool.Run(context.Background(), json.RawMessage(tcse.input), tc)
			if !errors.Is(err, ErrInvalidInput) ||
				!strings.Contains(err.Error(), "unknown or inaccessible field") ||
				strings.Contains(err.Error(), "mask") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestExpandUsesMaskedJoinFieldsInternally(t *testing.T) {
	t.Parallel()
	parent := entity.Entity{
		Name:       "users",
		Source:     "users",
		Attributes: []entity.Attribute{{Name: "email", Mask: "email"}},
		Relations: []entity.Relationship{{
			Name: "orders", Target: "orders", Cardinality: "many",
			JoinOn: map[string]string{"email": "user_email"},
		}},
		Role: entity.RoleAccess{entity.ActionRead: {"reader"}},
		MCP:  entity.MCPFlags{DMLTools: true},
	}
	target := entity.Entity{
		Name:       "orders",
		Source:     "orders",
		Attributes: []entity.Attribute{{Name: "id"}, {Name: "user_email", Mask: "email"}},
		Role:       entity.RoleAccess{entity.ActionRead: {"reader"}},
		MCP:        entity.MCPFlags{DMLTools: true},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{parent, target})
	queries := 0
	db := &store.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
		queries++
		if queries == 1 {
			return store.NewFakeRows([]string{"email"}, []any{"alice@example.com"}), nil
		}
		return store.NewFakeRows([]string{"id", "user_email"}, []any{int64(7), "alice@example.com"}), nil
	}}
	tc := Context{
		Role: "reader", Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg),
		Masker: mask.NewRuleMasker(nil),
		Sources: map[string]DataSource{
			"default": {DB: db, Dialect: testdialect.Postgres{}},
		},
	}
	result, err := (ReadTool{}).Run(context.Background(), json.RawMessage(`{"entity":"users","expand":["orders"]}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	children, ok := result.Content[0]["orders"].([]map[string]any)
	if !ok || len(children) != 1 || children[0]["id"] != int64(7) {
		t.Fatalf("expanded rows = %#v", result.Content)
	}
	if result.Content[0]["email"] != "a***@example.com" ||
		children[0]["user_email"] != "a***@example.com" {
		t.Fatalf("join fields were not safely masked: %#v", result.Content)
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

func TestReadToolCacheHit(t *testing.T) {
	t.Parallel()
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	auth := rbac.NewRoleAuthorizer(reg)
	calls := 0
	db := &store.FakeDB{QueryFn: func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
		calls++
		return store.NewFakeRows([]string{"id", "email"}, []any{int64(1), "a@x.com"}), nil
	}}
	sampler := &recordingAnalyzeSampler{}
	tc := Context{Role: "reader", DB: db, Dialect: testdialect.Postgres{}, Registry: reg, Authorizer: auth,
		Cache: cache.NewTTLCache[[]map[string]any](time.Minute, 0), Feedback: cost.NewMemoryStore(),
		Analyze: cost.AnalyzePolicy{
			Sampler: sampler,
			Config:  cost.AnalyzeConfig{Enabled: true, ReadOnly: true, SampleRate: 1, Timeout: time.Second},
		}}
	in, _ := json.Marshal(readInput{Entity: "users", Filter: []condJSON{{Field: "id", Op: "eq", Value: 1}}})
	_, _ = ReadTool{}.Run(context.Background(), in, tc)
	_, _ = ReadTool{}.Run(context.Background(), in, tc)
	if calls != 1 {
		t.Fatalf("DB called %d times, want 1 (cache hit)", calls)
	}
	if len(sampler.calls) != 1 {
		t.Fatalf("EXPLAIN ANALYZE called %d times, want only the real DB read", len(sampler.calls))
	}
}

func TestTransactionReadBypassesGlobalCache(t *testing.T) {
	t.Parallel()
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	auth := rbac.NewRoleAuthorizer(reg)
	global := &store.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
		return store.NewFakeRows([]string{"id"}, []any{int64(1)}), nil
	}}
	tx := &queryTx{query: func(context.Context, string, ...any) (store.Rows, error) {
		return store.NewFakeRows([]string{"id"}, []any{int64(2)}), nil
	}}
	beginner := &store.FakeDB{BeginFn: func(context.Context, *store.TxOptions) (store.Tx, error) {
		return tx, nil
	}}
	manager := NewTransactionManager(time.Minute, 2)
	defer manager.Close()
	cc := cache.NewTTLCache[[]map[string]any](time.Minute, 0)
	tc := Context{
		Role: "reader", DB: global, Dialect: testdialect.Postgres{}, Registry: reg, Authorizer: auth, Cache: cc,
		Sources:      map[string]DataSource{"default": {DB: global, Dialect: testdialect.Postgres{}}},
		Transactions: manager,
	}
	base, _ := json.Marshal(readInput{Entity: "users", Fields: []string{"id"}})
	if _, err := (ReadTool{}).Run(context.Background(), base, tc); err != nil {
		t.Fatal(err)
	}
	token, err := manager.Begin(context.Background(), beginner, "", "reader", nil, "default", nil)
	if err != nil {
		t.Fatal(err)
	}
	inside, _ := json.Marshal(readInput{Entity: "users", Fields: []string{"id"}, Transaction: token})
	result, err := (ReadTool{}).Run(context.Background(), inside, tc)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content[0]["id"] != int64(2) {
		t.Fatalf("transaction read used global cache: %#v", result.Content)
	}
}

func TestReadToolEntityNotFound(t *testing.T) {
	t.Parallel()
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	tc := Context{Role: "reader", Dialect: testdialect.Postgres{}, Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg)}
	in, _ := json.Marshal(readInput{Entity: "ghost"})
	_, err := ReadTool{}.Run(context.Background(), in, tc)
	if !errors.Is(err, ErrEntityNotFound) {
		t.Fatalf("got %v, want ErrEntityNotFound", err)
	}
}

func TestReadToolKeyset(t *testing.T) {
	t.Parallel()
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	auth := rbac.NewRoleAuthorizer(reg)
	db := &store.FakeDB{QueryFn: func(_ context.Context, q string, _ ...any) (store.Rows, error) {
		if !strings.Contains(q, ">") {
			t.Errorf("expected keyset '>' in SQL, got %q", q)
		}
		return store.NewFakeRows([]string{"id", "email"}, []any{int64(2), "bob@x.com"}), nil
	}}
	tc := Context{Role: "reader", DB: db, Dialect: testdialect.Postgres{}, Registry: reg, Authorizer: auth}
	in, _ := json.Marshal(readInput{Entity: "users", Cursor: map[string]any{"id": int64(1)}, Limit: 10})
	res, err := ReadTool{}.Run(context.Background(), in, tc)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Content) != 1 || res.Content[0]["id"] != int64(2) {
		t.Fatalf("got %v", res.Content)
	}
}

func TestCreateToolUnauthorized(t *testing.T) {
	t.Parallel()
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	tc := Context{Role: "nobody", Dialect: testdialect.Postgres{}, Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg)}
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

func TestReadToolCompositeKeyset(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name: "events", Source: "events",
		Attributes: []entity.Attribute{{Name: "a"}, {Name: "b"}},
		Keys:       []entity.Key{{Columns: []string{"a", "b"}, Primary: true}},
		Role:       entity.RoleAccess{entity.ActionRead: {"reader"}},
		MCP:        entity.MCPFlags{DMLTools: true},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	auth := rbac.NewRoleAuthorizer(reg)
	var gotSQL string
	db := &store.FakeDB{QueryFn: func(_ context.Context, q string, _ ...any) (store.Rows, error) {
		gotSQL = q
		return store.NewFakeRows([]string{"a", "b"}, []any{int64(1), int64(2)}), nil
	}}
	tc := Context{Role: "reader", DB: db, Dialect: testdialect.Postgres{}, Registry: reg, Authorizer: auth}
	in, _ := json.Marshal(readInput{Entity: "events", Cursor: map[string]any{"a": int64(1), "b": int64(2)}, Limit: 10})
	_, err := ReadTool{}.Run(context.Background(), in, tc)
	if err != nil {
		t.Fatal(err)
	}
	// Composite keyset must OR-expand `a>? OR (a=? AND b>?)` and ORDER BY a, b
	// so no rows are skipped.
	if !strings.Contains(gotSQL, " OR ") || !strings.Contains(gotSQL, "ORDER BY") {
		t.Fatalf("composite keyset SQL missing OR/ORDER BY: %q", gotSQL)
	}
}

func TestArgsKeyNoCollision(t *testing.T) {
	t.Parallel()
	if argsKey([]any{"a b", "c"}) == argsKey([]any{"a", "b c"}) {
		t.Fatal("string-boundary collision in cache key")
	}
	if argsKey([]any{"1", 2}) == argsKey([]any{1, "2"}) {
		t.Fatal("string/number collision in cache key")
	}
}

func TestReadToolRejectsHiddenFieldFilter(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name: "users", Source: "users",
		Attributes: []entity.Attribute{{Name: "id"}, {Name: "salary", Excluded: true}},
		Keys:       []entity.Key{{Columns: []string{"id"}, Primary: true}},
		Role:       entity.RoleAccess{entity.ActionRead: {"reader"}},
		MCP:        entity.MCPFlags{DMLTools: true},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	auth := rbac.NewRoleAuthorizer(reg)
	db := &store.FakeDB{QueryFn: func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
		t.Fatal("query must not run when filtering a hidden field")
		return nil, nil
	}}
	tc := Context{Role: "reader", DB: db, Dialect: testdialect.Postgres{}, Registry: reg, Authorizer: auth}
	in, _ := json.Marshal(readInput{Entity: "users", Filter: []condJSON{{Field: "salary", Op: "gt", Value: 100000}}})
	_, err := ReadTool{}.Run(context.Background(), in, tc)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("got %v, want ErrInvalidInput for hidden-field filter", err)
	}
}

func TestRunToolWiresHooksAndAudit(t *testing.T) {
	t.Parallel()
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	auth := rbac.NewRoleAuthorizer(reg)
	db := &store.FakeDB{QueryFn: func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
		return store.NewFakeRows([]string{"id", "email"}, []any{int64(1), "a@x.com"}), nil
	}}
	var before, after bool
	hooks := &hook.Hooks{
		BeforeTool: func(ctx context.Context, _ string, _ json.RawMessage) context.Context { before = true; return ctx },
		AfterTool:  func(_ context.Context, _ string, _ any, _ error) { after = true },
	}
	aud := &recorderAuditor{}
	tc := Context{
		Role: "reader", DB: db, Dialect: testdialect.Postgres{}, Registry: reg,
		Authorizer: auth, Masker: mask.NewRuleMasker(nil), Hooks: hooks, Auditor: aud,
	}
	in, _ := json.Marshal(readInput{Entity: "users", Filter: []condJSON{{Field: "id", Op: "eq", Value: int64(1)}}})
	res, err := RunTool(context.Background(), ReadTool{}, in, tc)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("rows = %v", res.Content)
	}
	if !before || !after {
		t.Fatalf("hooks not fired: before=%v after=%v", before, after)
	}
	if len(aud.events) != 1 || aud.events[0].Tool != "read_records" || !aud.events[0].Allowed {
		t.Fatalf("audit not recorded: %+v", aud.events)
	}
}

func TestRunToolEnforcesBudgetAndAuditsRejection(t *testing.T) {
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	db := &store.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
		return store.NewFakeRows(
			[]string{"id", "email"},
			[]any{int64(1), "a@x.com"},
			[]any{int64(2), "b@x.com"},
		), nil
	}}
	auditor := &recorderAuditor{}
	tc := Context{
		Role: "reader", DB: db, Dialect: testdialect.Postgres{}, Registry: reg,
		Authorizer: rbac.NewRoleAuthorizer(reg), Auditor: auditor,
		Budget: budget.New(map[string]budget.Limits{
			"reader": {MaxReturnedRows: 1},
		}, nil),
	}
	input, _ := json.Marshal(readInput{Entity: "users"})
	_, err := RunTool(context.Background(), ReadTool{}, input, tc)
	if !errors.Is(err, budget.ErrExceeded) {
		t.Fatalf("error = %v", err)
	}
	if len(auditor.events) != 1 || auditor.events[0].Allowed {
		t.Fatalf("audit = %+v", auditor.events)
	}
}

func TestRunToolEngineBackpressure(t *testing.T) {
	t.Parallel()
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	auth := rbac.NewRoleAuthorizer(reg)
	eng, _ := engine.New(engine.WithIOPool(1), engine.WithMaxInflight(1))
	block := make(chan struct{})
	done := make(chan struct{})
	go func() {
		_, _ = eng.Submit(context.Background(), "", func(_ context.Context) (any, error) { <-block; return nil, nil })
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	db := &store.FakeDB{QueryFn: func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
		return store.NewFakeRows([]string{"id"}, []any{int64(1)}), nil
	}}
	tc := Context{Role: "reader", DB: db, Dialect: testdialect.Postgres{}, Registry: reg, Authorizer: auth, Engine: eng}
	in, _ := json.Marshal(readInput{Entity: "users", Filter: []condJSON{{Field: "id", Op: "eq", Value: int64(1)}}})
	_, err := RunTool(context.Background(), ReadTool{}, in, tc)
	if !errors.Is(err, engine.ErrOverloaded) {
		t.Fatalf("got %v, want ErrOverloaded (engine wired into RunTool)", err)
	}
	close(block)
	<-done
}

func TestRunToolValidatesTransactionBeforeSingleflight(t *testing.T) {
	manager := NewTransactionManager(time.Minute, 2)
	defer manager.Close()
	tx := &store.FakeTx{}
	beginner := &store.FakeDB{BeginFn: func(context.Context, *store.TxOptions) (store.Tx, error) {
		return tx, nil
	}}
	token, err := manager.Begin(context.Background(), beginner, "session-a", "reader", nil, "default", nil)
	if err != nil {
		t.Fatal(err)
	}
	eng, _ := engine.New(engine.WithIOPool(2), engine.WithMaxInflight(4))
	started := make(chan struct{})
	release := make(chan struct{})
	probe := blockingTransactionReadTool{started: started, release: release}
	input := json.RawMessage(`{"transaction":"` + token + `"}`)
	validDone := make(chan error, 1)
	go func() {
		_, err := RunTool(context.Background(), probe, input, Context{
			Role: "reader", Session: "session-a", Transactions: manager, Engine: eng,
		})
		validDone <- err
	}()
	<-started
	_, err = RunTool(context.Background(), probe, input, Context{
		Role: "reader", Session: "session-b", Transactions: manager, Engine: eng,
	})
	if !errors.Is(err, ErrTransactionScope) {
		t.Fatalf("cross-session transaction error = %v, want scope mismatch", err)
	}
	close(release)
	if err := <-validDone; err != nil {
		t.Fatal(err)
	}
}

func TestAuthorizePrecedesGateAndHooksFire(t *testing.T) {
	t.Parallel()
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	events := []string{}
	hooks := &hook.Hooks{
		OnAuthorize: func(context.Context, rbac.Request, rbac.Decision) {
			events = append(events, "authorize-hook")
		},
		OnCostGate: func(context.Context, cost.Plan, cost.Score, string) {
			events = append(events, "gate-hook")
		},
	}
	db := &store.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
		return store.NewFakeRows([]string{"id"}, []any{int64(1)}), nil
	}}
	tc := Context{
		Role: "reader", DB: db, Dialect: testdialect.Postgres{}, Registry: reg,
		Authorizer: recordingAuthorizer{events: &events, dec: rbac.Decision{Allowed: true, Fields: []string{"id"}}},
		Gate:       recordingGate{events: &events}, Hooks: hooks,
	}
	in, _ := json.Marshal(readInput{Entity: "users", Fields: []string{"id"}})
	if _, err := (ReadTool{}).Run(context.Background(), in, tc); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(events, ","); got != "authorize,authorize-hook,gate,gate-hook" {
		t.Fatalf("event order = %s", got)
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

func TestReadCacheIsolatedByAuthorizationScope(t *testing.T) {
	t.Parallel()
	e := testUsersEntity()
	e.Role[entity.ActionRead] = []string{"tenant-a", "tenant-b"}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	calls := 0
	db := &store.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
		calls++
		return store.NewFakeRows([]string{"id"}, []any{int64(calls)}), nil
	}}
	cc := cache.NewTTLCache[[]map[string]any](time.Minute, 0)
	in, _ := json.Marshal(readInput{Entity: "users", Fields: []string{"id"}})
	scopes := []struct {
		role    string
		subject map[string]any
	}{
		{"tenant-a", map[string]any{"tenant_id": 1}},
		{"tenant-a", map[string]any{"tenant_id": 2}},
		{"tenant-b", map[string]any{"tenant_id": 1}},
		{"tenant-a", map[string]any{"tenant_id": 1}},
		{"tenant-a", map[string]any{"tenant_id": 2}},
		{"tenant-b", map[string]any{"tenant_id": 1}},
	}
	for _, scope := range scopes {
		tc := Context{
			Role: scope.role, Subject: scope.subject, DB: db, Dialect: testdialect.Postgres{}, Registry: reg,
			Authorizer: rbac.NewRoleAuthorizer(reg), Cache: cc,
		}
		if _, err := (ReadTool{}).Run(context.Background(), in, tc); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 3 {
		t.Fatalf("DB called %d times, want one isolated cache fill per role/subject", calls)
	}
}

func TestDecodeInputPreservesLargeInteger(t *testing.T) {
	var input readInput
	err := decodeInput([]byte(`{"entity":"users","filter":[{"field":"id","op":"eq","value":9007199254740993}]}`), &input)
	if err != nil {
		t.Fatal(err)
	}
	predicate, err := filterToPredicate(input.Filter)
	if err != nil {
		t.Fatal(err)
	}
	condition, ok := predicate.(relalg.Condition)
	if !ok || condition.Value != int64(9007199254740993) {
		t.Fatalf("condition = %#v", predicate)
	}
}

func TestRunToolEnforcesReturnedByteLimit(t *testing.T) {
	entityConfig := testUsersEntity()
	registry, _ := entity.NewRegistry([]entity.Entity{entityConfig})
	db := &store.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
		return store.NewFakeRows([]string{"id", "email"}, []any{int64(1), strings.Repeat("x", 64)}), nil
	}}
	input := json.RawMessage(`{"entity":"users","fields":["id","email"]}`)
	_, err := RunTool(context.Background(), ReadTool{}, input, Context{
		Role: "reader", DB: db, Dialect: testdialect.Postgres{}, Registry: registry,
		Authorizer: rbac.NewRoleAuthorizer(registry), MaxReturnedBytes: 16,
	})
	if !errors.Is(err, budget.ErrExceeded) {
		t.Fatalf("error = %v, want budget exceeded", err)
	}
}
