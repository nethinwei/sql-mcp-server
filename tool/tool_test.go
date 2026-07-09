package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/audit"
	"github.com/nethinwei/sql-mcp-server/cache"
	"github.com/nethinwei/sql-mcp-server/config"
	"github.com/nethinwei/sql-mcp-server/cost"
	"github.com/nethinwei/sql-mcp-server/dialect"
	"github.com/nethinwei/sql-mcp-server/engine"
	"github.com/nethinwei/sql-mcp-server/entity"
	"github.com/nethinwei/sql-mcp-server/hook"
	"github.com/nethinwei/sql-mcp-server/mask"
	"github.com/nethinwei/sql-mcp-server/rbac"
	"github.com/nethinwei/sql-mcp-server/store"
)

// recorderAuditor is a synchronous audit.Auditor fake for tests.
type recorderAuditor struct{ events []audit.Event }

func (r *recorderAuditor) Record(_ context.Context, e audit.Event) error {
	r.events = append(r.events, e)
	return nil
}

func testUsersEntity() entity.Entity {
	return entity.Entity{
		Name:       "users",
		Source:     "users",
		Attributes: []entity.Attribute{{Name: "id"}, {Name: "email", Mask: "email"}},
		Keys:       []entity.Key{{Columns: []string{"id"}, Primary: true}},
		Role:       entity.RoleAccess{entity.ActionRead: {"reader"}, entity.ActionCreate: {"writer"}},
	}
}

func TestRegistryEnabledFilters(t *testing.T) {
	t.Parallel()
	r, _ := NewRegistry(DefaultTools())
	enabled := r.Enabled(config.DefaultToolFlags()) // delete off
	if len(enabled) != 6 {
		t.Fatalf("got %d tools, want 6 (delete off)", len(enabled))
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
		Role: "reader", DB: db, Dialect: dialect.Postgres{},
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
	tc := Context{Role: "reader", DB: db, Dialect: dialect.Postgres{}, Registry: reg, Authorizer: auth, Gate: gate}
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
	tc := Context{Role: "nobody", Dialect: dialect.Postgres{}, Registry: reg, Authorizer: auth}
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
	tc := Context{Role: "writer", DB: db, Dialect: dialect.MySQL{}, Registry: reg, Authorizer: auth}
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

func TestUpdateToolUnsafeWrite(t *testing.T) {
	t.Parallel()
	e := testUsersEntity()
	e.Role[entity.ActionUpdate] = []string{"writer"}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	auth := rbac.NewRoleAuthorizer(reg)
	tc := Context{Role: "writer", Dialect: dialect.Postgres{}, Registry: reg, Authorizer: auth}
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
	tc := Context{Registry: reg}
	in, _ := json.Marshal(map[string]any{"entity": "users"})
	res, err := DescribeTool{}.Run(context.Background(), in, tc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content[0]["name"] != "users" {
		t.Fatalf("got %v", res.Content[0])
	}
}

func TestExecuteToolCallsProcedure(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name: "sp", Source: "sp", Kind: entity.KindProcedure,
		Role: entity.RoleAccess{entity.ActionExecute: {"caller"}},
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
	tc := Context{Role: "caller", DB: db, Dialect: dialect.Postgres{}, Registry: reg, Authorizer: auth}
	in, _ := json.Marshal(executeInput{Entity: "sp", Args: map[string]any{"x": 1}})
	res, err := ExecuteTool{}.Run(context.Background(), in, tc)
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("procedure not called")
	}
	if len(res.Content) != 1 || res.Content[0]["n"] != int64(42) {
		t.Fatalf("got %v", res.Content)
	}
}

func TestExecuteToolRejectsNonProcedure(t *testing.T) {
	t.Parallel()
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	auth := rbac.NewRoleAuthorizer(reg)
	tc := Context{Role: "reader", Dialect: dialect.Postgres{}, Registry: reg, Authorizer: auth}
	in, _ := json.Marshal(executeInput{Entity: "users"})
	_, err := ExecuteTool{}.Run(context.Background(), in, tc)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("got %v, want ErrInvalidInput", err)
	}
}

func TestCreateToolReturning(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name: "users", Source: "users", Kind: entity.KindTable,
		Attributes: []entity.Attribute{{Name: "id"}, {Name: "email"}},
		Keys:       []entity.Key{{Columns: []string{"id"}, Primary: true}},
		Role:       entity.RoleAccess{entity.ActionCreate: {"writer"}},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	auth := rbac.NewRoleAuthorizer(reg)
	db := &store.FakeDB{QueryFn: func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
		return store.NewFakeRows([]string{"id"}, []any{int64(7)}), nil
	}}
	tc := Context{Role: "writer", DB: db, Dialect: dialect.Postgres{}, Registry: reg, Authorizer: auth}
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
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	auth := rbac.NewRoleAuthorizer(reg)
	db := &store.FakeDB{QueryFn: func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
		return store.NewFakeRows([]string{"dept", "count"}, []any{"eng", int64(5)}), nil
	}}
	tc := Context{Role: "reader", DB: db, Dialect: dialect.Postgres{}, Registry: reg, Authorizer: auth}
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
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	auth := rbac.NewRoleAuthorizer(reg)
	db := &store.FakeDB{QueryFn: func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
		t.Fatal("query must not run for an invalid aggregate func")
		return nil, nil
	}}
	tc := Context{Role: "reader", DB: db, Dialect: dialect.Postgres{}, Registry: reg, Authorizer: auth}
	in, _ := json.Marshal(aggregateInput{Entity: "users", Aggregates: []aggJSON{{Func: "count(*);drop"}}})
	_, err := AggregateTool{}.Run(context.Background(), in, tc)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("got %v, want ErrInvalidInput", err)
	}
}

func TestDeleteTool(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name: "users", Source: "users",
		Attributes: []entity.Attribute{{Name: "id"}},
		Keys:       []entity.Key{{Columns: []string{"id"}, Primary: true}},
		Role:       entity.RoleAccess{entity.ActionDelete: {"admin"}},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	auth := rbac.NewRoleAuthorizer(reg)
	db := &store.FakeDB{ExecFn: func(_ context.Context, _ string, _ ...any) (store.Result, error) {
		return store.Result{RowsAffected: 1}, nil
	}}
	tc := Context{Role: "admin", DB: db, Dialect: dialect.Postgres{}, Registry: reg, Authorizer: auth}
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
	tc := Context{Role: "reader", DB: db, Dialect: dialect.Postgres{}, Registry: reg, Authorizer: auth,
		Cache: cache.NewTTLCache[[]map[string]any](time.Minute, 0)}
	in, _ := json.Marshal(readInput{Entity: "users", Filter: []condJSON{{Field: "id", Op: "eq", Value: 1}}})
	_, _ = ReadTool{}.Run(context.Background(), in, tc)
	_, _ = ReadTool{}.Run(context.Background(), in, tc)
	if calls != 1 {
		t.Fatalf("DB called %d times, want 1 (cache hit)", calls)
	}
}

func TestReadToolEntityNotFound(t *testing.T) {
	t.Parallel()
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	tc := Context{Role: "reader", Dialect: dialect.Postgres{}, Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg)}
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
	tc := Context{Role: "reader", DB: db, Dialect: dialect.Postgres{}, Registry: reg, Authorizer: auth}
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
	tc := Context{Role: "nobody", Dialect: dialect.Postgres{}, Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg)}
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
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	auth := rbac.NewRoleAuthorizer(reg)
	db := &store.FakeDB{ExecFn: func(_ context.Context, _ string, _ ...any) (store.Result, error) {
		t.Fatal("non-PK write must not execute past the gate")
		return store.Result{}, nil
	}}
	gate := cost.NewGate(cost.StaticRule{PKWhitelist: true}, cost.WriteGuard{})
	tc := Context{Role: "writer", DB: db, Dialect: dialect.Postgres{}, Registry: reg, Authorizer: auth, Gate: gate}
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
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	auth := rbac.NewRoleAuthorizer(reg)
	var gotSQL string
	db := &store.FakeDB{QueryFn: func(_ context.Context, q string, _ ...any) (store.Rows, error) {
		gotSQL = q
		return store.NewFakeRows([]string{"a", "b"}, []any{int64(1), int64(2)}), nil
	}}
	tc := Context{Role: "reader", DB: db, Dialect: dialect.Postgres{}, Registry: reg, Authorizer: auth}
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
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	auth := rbac.NewRoleAuthorizer(reg)
	db := &store.FakeDB{QueryFn: func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
		t.Fatal("query must not run when filtering a hidden field")
		return nil, nil
	}}
	tc := Context{Role: "reader", DB: db, Dialect: dialect.Postgres{}, Registry: reg, Authorizer: auth}
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
		Role: "reader", DB: db, Dialect: dialect.Postgres{}, Registry: reg,
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
	tc := Context{Role: "reader", DB: db, Dialect: dialect.Postgres{}, Registry: reg, Authorizer: auth, Engine: eng}
	in, _ := json.Marshal(readInput{Entity: "users", Filter: []condJSON{{Field: "id", Op: "eq", Value: int64(1)}}})
	_, err := RunTool(context.Background(), ReadTool{}, in, tc)
	if !errors.Is(err, engine.ErrOverloaded) {
		t.Fatalf("got %v, want ErrOverloaded (engine wired into RunTool)", err)
	}
	close(block)
	<-done
}
