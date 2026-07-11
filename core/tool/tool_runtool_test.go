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

func TestRunToolStampsDecisionID(t *testing.T) {
	t.Parallel()
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	db := &store.FakeDB{QueryFn: func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
		return store.NewFakeRows([]string{"id", "email"}, []any{int64(1), "a@x.com"}), nil
	}}
	var ctxID string
	hooks := &hook.Hooks{
		BeforeTool: func(ctx context.Context, _ string, _ json.RawMessage) context.Context {
			ctxID = DecisionIDFromContext(ctx)
			return ctx
		},
	}
	aud := &recorderAuditor{}
	tc := Context{
		Role: "reader", DB: db, Dialect: testdialect.Postgres{}, Registry: reg,
		Authorizer: rbac.NewRoleAuthorizer(reg), Hooks: hooks, Auditor: aud,
		DecisionID: "fixed-decision-id",
	}
	in, _ := json.Marshal(readInput{Entity: "users", Filter: []condJSON{{Field: "id", Op: "eq", Value: int64(1)}}})
	if _, err := RunTool(context.Background(), ReadTool{}, in, tc); err != nil {
		t.Fatal(err)
	}
	if ctxID != "fixed-decision-id" {
		t.Fatalf("hook context decision ID = %q", ctxID)
	}
	if len(aud.events) != 1 || aud.events[0].DecisionID != "fixed-decision-id" {
		t.Fatalf("audit decision ID = %+v", aud.events)
	}
	tc.DecisionID = ""
	aud.events = nil
	if _, err := RunTool(context.Background(), ReadTool{}, in, tc); err != nil {
		t.Fatal(err)
	}
	if len(aud.events) != 1 || aud.events[0].DecisionID == "" {
		t.Fatalf("RunTool must generate a decision ID when unset: %+v", aud.events)
	}
}

// TestRunToolAuditRecordsEntityActionAndCode guards the frozen audit schema:
// a denied call must carry the entity, the action, and the stable denial code
// so the audit line alone explains the rejection.
func TestRunToolAuditRecordsEntityActionAndCode(t *testing.T) {
	t.Parallel()
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	aud := &recorderAuditor{}
	tc := Context{
		Role: "intruder", Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg),
		Auditor: aud, Masker: mask.NewRuleMasker(nil),
	}
	in, _ := json.Marshal(readInput{Entity: "users"})
	if _, err := RunTool(context.Background(), ReadTool{}, in, tc); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("error = %v, want unauthorized", err)
	}
	if len(aud.events) != 1 {
		t.Fatalf("audit events = %+v", aud.events)
	}
	e := aud.events[0]
	if e.Entity != "users" || e.Action != "read" || e.Code != CodeUnauthorized || e.Allowed {
		t.Fatalf("audit event = %+v, want entity=users action=read code=%s", e, CodeUnauthorized)
	}
}

// TestRunToolAuditRecordsSuccessEntityAction guards the allowed path: entity
// and action must be recorded with an empty code.
func TestRunToolAuditRecordsSuccessEntityAction(t *testing.T) {
	t.Parallel()
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	db := &store.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
		return store.NewFakeRows([]string{"id"}, []any{int64(1)}), nil
	}}
	aud := &recorderAuditor{}
	tc := Context{
		Role: "reader", DB: db, Dialect: testdialect.Postgres{}, Registry: reg,
		Authorizer: rbac.NewRoleAuthorizer(reg), Auditor: aud, Masker: mask.NewRuleMasker(nil),
	}
	in, _ := json.Marshal(readInput{Entity: "users", Filter: []condJSON{{Field: "id", Op: "eq", Value: int64(1)}}})
	if _, err := RunTool(context.Background(), ReadTool{}, in, tc); err != nil {
		t.Fatal(err)
	}
	e := aud.events[0]
	if e.Entity != "users" || e.Action != "read" || e.Code != "" || !e.Allowed {
		t.Fatalf("audit event = %+v", e)
	}
}

type denyingBudget struct{}

func (denyingBudget) Acquire(context.Context, budget.Scope) (budget.Lease, error) {
	return nil, budget.ErrExceeded
}

// TestRunToolBudgetAcquireDenialFiresHooks guards telemetry coverage of the
// earliest rejection path: a budget-acquire denial must still produce a span
// (BeforeTool with the decision ID in context) and record the error before
// the span ends.
func TestRunToolBudgetAcquireDenialFiresHooks(t *testing.T) {
	t.Parallel()
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	var events []string
	var ctxID string
	hooks := &hook.Hooks{
		BeforeTool: func(ctx context.Context, _ string, _ json.RawMessage) context.Context {
			events = append(events, "before")
			ctxID = DecisionIDFromContext(ctx)
			return ctx
		},
		OnError:   func(context.Context, error) { events = append(events, "error") },
		AfterTool: func(context.Context, string, any, error) { events = append(events, "after") },
	}
	aud := &recorderAuditor{}
	tc := Context{
		Role: "reader", Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg),
		Hooks: hooks, Auditor: aud, Budget: denyingBudget{},
	}
	input, _ := json.Marshal(readInput{Entity: "users"})
	_, err := RunTool(context.Background(), ReadTool{}, input, tc)
	if !errors.Is(err, budget.ErrExceeded) {
		t.Fatalf("error = %v", err)
	}
	if len(events) != 3 || events[0] != "before" || events[1] != "error" || events[2] != "after" {
		t.Fatalf("hook sequence = %v, want [before error after]", events)
	}
	if ctxID == "" {
		t.Fatal("BeforeTool context must carry the decision ID")
	}
	if len(aud.events) != 1 || aud.events[0].DecisionID == "" || aud.events[0].Allowed {
		t.Fatalf("audit = %+v", aud.events)
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
