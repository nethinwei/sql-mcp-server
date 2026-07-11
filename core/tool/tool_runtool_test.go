package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/budget"
	"github.com/nethinwei/sql-mcp-server/core/engine"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/hook"
	"github.com/nethinwei/sql-mcp-server/core/mask"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/internal/testdialect"
)

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
