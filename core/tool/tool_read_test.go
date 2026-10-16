package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/budget"
	"github.com/nethinwei/sql-mcp-server/core/cache"
	"github.com/nethinwei/sql-mcp-server/core/codegen"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/cost/costtest"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/hook"
	"github.com/nethinwei/sql-mcp-server/core/mask"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/core/store/storetest"
	"github.com/nethinwei/sql-mcp-server/internal/testdialect"
)

func TestReadToolEndToEnd(t *testing.T) {
	t.Parallel()
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	auth := rbac.NewRoleAuthorizer(reg)
	db := &storetest.FakeDB{QueryFn: func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
		return storetest.NewFakeRows([]string{"id", "email"}, []any{int64(1), "alice@x.com"}), nil
	}}
	tc := Context{
		Services: &Services{Registry: reg, Authorizer: auth, Masker: mask.NewRuleMasker()},
		Role:     "reader",
		DB:       db,
		Dialect:  testdialect.Postgres{},
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
	db := &storetest.FakeDB{QueryFn: func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
		return storetest.NewFakeRows([]string{"id", "email"}, []any{int64(1), "a@x.com"}), nil
	}}
	feedback := cost.NewMemoryStore()
	tc := Context{
		Services: &Services{Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg), Feedback: feedback},
		Role:     "reader",
		DB:       db,
		Dialect:  testdialect.Postgres{},
		Analyze: cost.AnalyzePolicy{
			Sampler: costtest.FakeAnalyzeSampler{Plan: cost.Plan{ActualRows: 25, ExecutionTime: 3 * time.Millisecond}},
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
	db := &storetest.FakeDB{QueryFn: func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
		return storetest.NewFakeRows([]string{"id", "email"}, []any{int64(1), "a@x.com"}), nil
	}}
	auditor := &recorderAuditor{}
	var hookErr error
	tc := Context{
		Services: &Services{
			Registry:   reg,
			Authorizer: rbac.NewRoleAuthorizer(reg),
			Feedback:   cost.NewMemoryStore(),
			Auditor:    auditor,
			Hooks:      &hook.Hooks{OnError: func(_ context.Context, err error) { hookErr = err }},
		},
		Role:    "reader",
		DB:      db,
		Dialect: testdialect.Postgres{},
		Analyze: cost.AnalyzePolicy{
			Sampler: costtest.FakeAnalyzeSampler{Err: errors.New("sampling timeout")},
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
	mainDB := &storetest.FakeDB{}
	archiveDB := &storetest.FakeDB{QueryFn: func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
		return storetest.NewFakeRows([]string{"id"}, []any{int64(1)}), nil
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
		Services: &Services{
			Registry:   reg,
			Authorizer: rbac.NewRoleAuthorizer(reg),
			Feedback:   cost.NewMemoryStore(),
			Sources: map[string]DataSource{
				"main":    {DB: mainDB, Dialect: testdialect.Postgres{}, Analyze: policy(mainSampler)},
				"archive": {DB: archiveDB, Dialect: testdialect.Postgres{}, Analyze: policy(archiveSampler)},
			},
		},
		Role: "reader",
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
	db := &storetest.FakeDB{}
	queries := 0
	db.QueryFn = func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
		queries++
		if queries == 1 {
			return storetest.NewFakeRows(
				[]string{"id", "email"},
				[]any{int64(1), "a@x.com"},
				[]any{int64(2), "b@x.com"},
			), nil
		}
		return storetest.NewFakeRows([]string{"id", "user_id"}, []any{int64(10), int64(1)}, []any{int64(11), int64(1)}), nil
	}
	auth := rbac.NewRoleAuthorizer(reg)
	tc := Context{Role: "reader", DB: db, Dialect: testdialect.Postgres{}, Services: &Services{
		Registry: reg, Authorizer: auth, Sources: map[string]DataSource{"default": {DB: db, Dialect: testdialect.Postgres{}}},
	}}
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
	db := &storetest.FakeDB{QueryFn: func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
		t.Fatal("query should not execute when gate rejects")
		return nil, nil
	}}
	gate := cost.NewGate(cost.Estimate{
		Explainer: costtest.FakeExplainer{Plan: cost.Plan{ScanType: cost.ScanFull, StatsFresh: true}},
		Threshold: cost.Threshold{RejectFullScan: true},
	})
	tc := Context{
		Services: &Services{Registry: reg, Authorizer: auth},
		Role:     "reader",
		DB:       db,
		Dialect:  testdialect.Postgres{},
		Gate:     gate,
	}
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
	tc := Context{Services: &Services{Registry: reg, Authorizer: auth}, Role: "nobody", Dialect: testdialect.Postgres{}}
	in, _ := json.Marshal(readInput{Entity: "users"})
	_, err := ReadTool{}.Run(context.Background(), in, tc)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("got %v, want ErrUnauthorized", err)
	}
}

func TestReadToolCacheHit(t *testing.T) {
	t.Parallel()
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	auth := rbac.NewRoleAuthorizer(reg)
	calls := 0
	db := &storetest.FakeDB{QueryFn: func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
		calls++
		return storetest.NewFakeRows([]string{"id", "email"}, []any{int64(1), "a@x.com"}), nil
	}}
	sampler := &recordingAnalyzeSampler{}
	tc := Context{
		Services: &Services{
			Registry:   reg,
			Authorizer: auth,
			Cache:      cache.NewTTLCache[[]map[string]any](time.Minute, 0),
			Feedback:   cost.NewMemoryStore(),
		},
		Role:    "reader",
		DB:      db,
		Dialect: testdialect.Postgres{},
		Analyze: cost.AnalyzePolicy{
			Sampler: sampler,
			Config:  cost.AnalyzeConfig{Enabled: true, ReadOnly: true, SampleRate: 1, Timeout: time.Second},
		},
	}
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
	global := &storetest.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
		return storetest.NewFakeRows([]string{"id"}, []any{int64(1)}), nil
	}}
	tx := &queryTx{query: func(context.Context, string, ...any) (store.Rows, error) {
		return storetest.NewFakeRows([]string{"id"}, []any{int64(2)}), nil
	}}
	beginner := &storetest.FakeDB{BeginFn: func(context.Context, *store.TxOptions) (store.Tx, error) {
		return tx, nil
	}}
	manager := NewTransactionManager(time.Minute, 2)
	defer manager.Close()
	cc := cache.NewTTLCache[[]map[string]any](time.Minute, 0)
	tc := Context{
		Services: &Services{
			Registry: reg, Authorizer: auth, Cache: cc,
			Sources:      map[string]DataSource{"default": {DB: global, Dialect: testdialect.Postgres{}}},
			Transactions: manager,
		},
		Role: "reader", DB: global, Dialect: testdialect.Postgres{},
	}
	base, _ := json.Marshal(readInput{Entity: "users", Fields: []string{"id"}})
	if _, err := (ReadTool{}).Run(context.Background(), base, tc); err != nil {
		t.Fatal(err)
	}
	token, err := manager.Begin(
		context.Background(),
		beginner,
		Caller{Role: "reader"},
		"default",
		nil,
	)
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

// An entity whose table is gone is refused before any SQL, with its own
// code for an authorized caller; another caller still sees UNAUTHORIZED.
func TestReadToolEntityUnavailable(t *testing.T) {
	t.Parallel()
	users := testUsersEntity()
	users.Unavailable = "its table is missing from the database"
	reg, _ := entity.NewRegistry([]entity.Entity{users})
	tc := Context{
		Services: &Services{Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg)},
		Role:     "reader",
		Dialect:  testdialect.Postgres{},
	}
	in, _ := json.Marshal(readInput{Entity: "users", Fields: []string{"id"}})
	_, err := ReadTool{}.Run(context.Background(), in, tc)
	if !errors.Is(err, ErrEntityUnavailable) {
		t.Fatalf("got %v, want ErrEntityUnavailable", err)
	}
	if d, ok := DenialFor(err, "d"); !ok || d.Code != CodeEntityUnavailable || d.Retryable ||
		!strings.Contains(d.Reason, "missing from the database") {
		t.Fatalf("denial = %+v", d)
	}
	tc.Role = "writer"
	if _, err := (ReadTool{}).Run(context.Background(), in, tc); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("an unauthorized caller got %v, want ErrUnauthorized", err)
	}
}

func TestReadToolEntityNotFound(t *testing.T) {
	t.Parallel()
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	tc := Context{
		Services: &Services{Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg)},
		Role:     "reader",
		Dialect:  testdialect.Postgres{},
	}
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
	db := &storetest.FakeDB{QueryFn: func(_ context.Context, q string, _ ...any) (store.Rows, error) {
		if !strings.Contains(q, ">") {
			t.Errorf("expected keyset '>' in SQL, got %q", q)
		}
		return storetest.NewFakeRows([]string{"id", "email"}, []any{int64(2), "bob@x.com"}), nil
	}}
	tc := Context{
		Services: &Services{Registry: reg, Authorizer: auth},
		Role:     "reader",
		DB:       db,
		Dialect:  testdialect.Postgres{},
	}
	in, _ := json.Marshal(readInput{Entity: "users", Cursor: map[string]any{"id": int64(1)}, Limit: 10})
	res, err := ReadTool{}.Run(context.Background(), in, tc)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Content) != 1 || res.Content[0]["id"] != int64(2) {
		t.Fatalf("got %v", res.Content)
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
	db := &storetest.FakeDB{QueryFn: func(_ context.Context, q string, _ ...any) (store.Rows, error) {
		gotSQL = q
		return storetest.NewFakeRows([]string{"a", "b"}, []any{int64(1), int64(2)}), nil
	}}
	tc := Context{
		Services: &Services{Registry: reg, Authorizer: auth},
		Role:     "reader",
		DB:       db,
		Dialect:  testdialect.Postgres{},
	}
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

func TestBuildReadExpressionKeysetSort(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name: "events", Source: "events",
		Attributes: []entity.Attribute{{Name: "a"}, {Name: "b"}},
		Keys:       []entity.Key{{Columns: []string{"a", "b"}, Primary: true}},
		Role:       entity.RoleAccess{entity.ActionRead: {"reader"}},
		MCP:        entity.MCPFlags{DMLTools: true},
	}
	res := entity.Resolved{Entity: e}
	in := readInput{
		Entity: "events",
		Cursor: map[string]any{"a": int64(1), "b": int64(2)},
		Limit:  10,
	}
	plan := readPlan{
		grant: grant{rbac.Decision{Allowed: true, Fields: []string{"a", "b"}}},
	}
	plan.full, plan.keysetCols = keysetAfter(e.PrimaryKey(), in.Cursor)
	expr := buildReadExpression(res, in, plan)
	compiled, err := codegen.Renderer{Dialect: testdialect.Postgres{}}.Compile(expr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compiled.SQL, "ORDER BY") {
		t.Fatalf("buildReadExpression must add ORDER BY for keyset cursor, got %q", compiled.SQL)
	}
	if !strings.Contains(compiled.SQL, " OR ") {
		t.Fatalf("buildReadExpression must OR-expand composite keyset, got %q", compiled.SQL)
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
	db := &storetest.FakeDB{QueryFn: func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
		t.Fatal("query must not run when filtering a hidden field")
		return nil, nil
	}}
	tc := Context{
		Services: &Services{Registry: reg, Authorizer: auth},
		Role:     "reader",
		DB:       db,
		Dialect:  testdialect.Postgres{},
	}
	in, _ := json.Marshal(readInput{Entity: "users", Filter: []condJSON{{Field: "salary", Op: "gt", Value: 100000}}})
	_, err := ReadTool{}.Run(context.Background(), in, tc)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("got %v, want ErrInvalidInput for hidden-field filter", err)
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
	db := &storetest.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
		queries++
		if queries == 1 {
			return storetest.NewFakeRows([]string{"email"}, []any{"alice@example.com"}), nil
		}
		return storetest.NewFakeRows([]string{"id", "user_email"}, []any{int64(7), "alice@example.com"}), nil
	}}
	tc := Context{
		Role: "reader", Services: &Services{
			Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg), Masker: mask.NewRuleMasker(),
			Sources: map[string]DataSource{"default": {DB: db, Dialect: testdialect.Postgres{}}},
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

func TestReadCacheIsolatedByAuthorizationScope(t *testing.T) {
	t.Parallel()
	e := testUsersEntity()
	e.Role[entity.ActionRead] = []string{"tenant-a", "tenant-b"}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	calls := 0
	db := &storetest.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
		calls++
		return storetest.NewFakeRows([]string{"id"}, []any{int64(calls)}), nil
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
			Services: &Services{Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg), Cache: cc},
			Role:     scope.role,
			Subject:  scope.subject,
			DB:       db,
			Dialect:  testdialect.Postgres{},
		}
		if _, err := (ReadTool{}).Run(context.Background(), in, tc); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 3 {
		t.Fatalf("DB called %d times, want one isolated cache fill per role/subject", calls)
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
	db := &storetest.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
		return storetest.NewFakeRows([]string{"id"}, []any{int64(1)}), nil
	}}
	tc := Context{
		Services: &Services{
			Registry:   reg,
			Authorizer: recordingAuthorizer{events: &events, dec: rbac.Decision{Allowed: true, Fields: []string{"id"}}},
			Hooks:      hooks,
		},
		Role:    "reader",
		DB:      db,
		Dialect: testdialect.Postgres{},
		Gate:    recordingGate{events: &events},
	}
	in, _ := json.Marshal(readInput{Entity: "users", Fields: []string{"id"}})
	if _, err := (ReadTool{}).Run(context.Background(), in, tc); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(events, ","); got != "authorize,authorize-hook,gate,gate-hook" {
		t.Fatalf("event order = %s", got)
	}
}

func TestReadRejectsOffsetWithoutLimit(t *testing.T) {
	t.Parallel()
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	tc := Context{Services: &Services{Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg)}, Role: "reader"}
	_, err := (ReadTool{}).Run(context.Background(), json.RawMessage(`{"entity":"users","offset":10}`), tc)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error = %v, want invalid input", err)
	}
}

// A negative limit would otherwise read past it as no limit at all,
// dropping the offset and the page bound with it.
func TestReadRejectsNegativeLimitOrOffset(t *testing.T) {
	t.Parallel()
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	tc := Context{Services: &Services{Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg)}, Role: "reader"}
	for _, input := range []string{
		`{"entity":"users","limit":-5,"offset":10}`, `{"entity":"users","limit":-1}`,
		`{"entity":"users","limit":5,"offset":-1}`,
	} {
		if _, err := (ReadTool{}).Run(context.Background(), json.RawMessage(input), tc); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%s: error = %v, want invalid input", input, err)
		}
	}
}

func TestReadCursorUsesIdentityKey(t *testing.T) {
	t.Parallel()
	base := entity.Entity{
		Name: "codes", Source: "codes", MCP: entity.MCPFlags{DMLTools: true},
		Attributes: []entity.Attribute{{Name: "code"}, {Name: "label"}},
		Role:       entity.RoleAccess{entity.ActionRead: {"reader"}},
	}
	byUnique := base
	byUnique.Keys = []entity.Key{{Name: "codes_code_key", Columns: []string{"code"}}}
	keyless := base
	keyless.Name, keyless.Source = "notes", "notes"
	reg, _ := entity.NewRegistry([]entity.Entity{byUnique, keyless})
	var sql string
	db := &storetest.FakeDB{QueryFn: func(_ context.Context, q string, _ ...any) (store.Rows, error) {
		sql = q
		return storetest.NewFakeRows([]string{"code"}), nil
	}}
	tc := Context{
		Services: &Services{Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg)},
		Role:     "reader",
		DB:       db,
		Dialect:  testdialect.Postgres{},
	}
	run := func(input string) error {
		_, err := (ReadTool{}).Run(context.Background(), json.RawMessage(input), tc)
		return err
	}
	if err := run(`{"entity":"codes","cursor":{"code":"a"},"limit":10}`); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, `"code" > $1`) || !strings.Contains(sql, `ORDER BY "code"`) {
		t.Fatalf("keyset on the unique key: %s", sql)
	}
	for input, why := range map[string]string{
		`{"entity":"notes","cursor":{"code":"a"},"limit":10}`:  "an entity without an identity key",
		`{"entity":"codes","cursor":{"label":"a"},"limit":10}`: "a cursor naming a non-key field",
	} {
		if err := run(input); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: error = %v, want invalid input", why, err)
		}
	}
}

func TestExpandMatchesMultiColumnAndDifferentlyTypedKeys(t *testing.T) {
	t.Parallel()
	orders := entity.Entity{
		Name: "orders", Source: "orders", MCP: entity.MCPFlags{DMLTools: true},
		Attributes: []entity.Attribute{{Name: "tenant_id"}, {Name: "id"}},
		Role:       entity.RoleAccess{entity.ActionRead: {"r"}},
		Relations: []entity.Relationship{{
			Name: "lines", Target: "lines", Cardinality: "has-many",
			JoinOn: map[string]string{"tenant_id": "tenant_id", "id": "order_id"},
		}},
	}
	lines := entity.Entity{
		Name: "lines", Source: "lines", MCP: entity.MCPFlags{DMLTools: true},
		Attributes: []entity.Attribute{{Name: "tenant_id"}, {Name: "order_id"}, {Name: "sku"}},
		Role:       entity.RoleAccess{entity.ActionRead: {"r"}},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{orders, lines})
	var childSQL string
	db := &storetest.FakeDB{QueryFn: func(_ context.Context, q string, _ ...any) (store.Rows, error) {
		if strings.Contains(q, `"lines"`) {
			childSQL = q
			// int64 keys from an int8 column, against int32 parent values.
			return storetest.NewFakeRows([]string{"tenant_id", "order_id", "sku"},
				[]any{int64(1), int64(10), "a"}, []any{int64(2), int64(10), "b"}), nil
		}
		return storetest.NewFakeRows([]string{"tenant_id", "id"},
			[]any{int32(1), int32(10)}, []any{int32(2), int32(10)}, []any{nil, int32(10)}), nil
	}}
	tc := Context{
		Services: &Services{Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg)},
		Role:     "r",
		DB:       db,
		Dialect:  testdialect.Postgres{},
	}
	res, err := (ReadTool{}).Run(context.Background(), json.RawMessage(`{"entity":"orders","expand":["lines"]}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(childSQL, `("order_id" = $1 AND "tenant_id" = $2) OR`) {
		t.Fatalf("child query = %s", childSQL)
	}
	for i, want := range []string{"a", "b", ""} {
		got := res.Content[i]["lines"].([]map[string]any)
		if want == "" && len(got) != 0 || want != "" && (len(got) != 1 || got[0]["sku"] != want) {
			t.Errorf("row %d lines = %v, want sku %q (a NULL key matches nothing)", i, got, want)
		}
	}
}

func TestJoinKeysDoNotCollide(t *testing.T) {
	t.Parallel()
	a, _ := joinKey("x\x1fs:y", "z")
	b, _ := joinKey("x", "y\x1fs:z")
	c, _ := joinKey("x", `y",s:"z`)
	if a == b || b == c || a == c {
		t.Fatalf("distinct tuples share a key: %q %q %q", a, b, c)
	}
	n, _ := joinKey(int32(7), []byte("k"))
	m, _ := joinKey(int64(7), "k")
	if n != m {
		t.Fatalf("driver typing changed the key: %q vs %q", n, m)
	}
}
