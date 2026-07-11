package tool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/cache"
	"github.com/nethinwei/sql-mcp-server/entity"
	"github.com/nethinwei/sql-mcp-server/rbac"
	"github.com/nethinwei/sql-mcp-server/store"
)

type failingCommitTx struct {
	store.FakeTx
	err error
}

func (t *failingCommitTx) Commit() error {
	t.Committed = true
	return t.err
}

func TestTransactionBindingCapacityAndCloseRollback(t *testing.T) {
	tx := &store.FakeTx{}
	db := &store.FakeDB{BeginFn: func(context.Context, *store.TxOptions) (store.Tx, error) {
		return tx, nil
	}}
	manager := NewTransactionManager(time.Minute, 1)
	subject := map[string]any{"tenant_id": "a"}
	token, err := manager.Begin(context.Background(), db, "session-a", "writer", subject, "primary", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.DB(token, "session-a", "reader", subject, "primary"); !errors.Is(err, ErrTransactionScope) {
		t.Fatalf("role mismatch error = %v", err)
	}
	if _, err := manager.DB(token, "session-a", "writer", map[string]any{"tenant_id": "b"}, "primary"); !errors.Is(err, ErrTransactionScope) {
		t.Fatalf("subject mismatch error = %v", err)
	}
	if _, err := manager.DB(token, "session-a", "writer", subject, "other"); !errors.Is(err, ErrTransactionScope) {
		t.Fatalf("datasource mismatch error = %v", err)
	}
	if _, err := manager.DB(token, "session-b", "writer", subject, "primary"); !errors.Is(err, ErrTransactionScope) {
		t.Fatalf("session mismatch error = %v", err)
	}
	if _, err := manager.Begin(context.Background(), db, "session-a", "writer", subject, "primary", nil); !errors.Is(err, ErrTransactionCapacity) {
		t.Fatalf("capacity error = %v", err)
	}
	manager.Close()
	if !tx.RolledBack {
		t.Fatal("Close did not rollback open transaction")
	}
}

func TestTransactionCommitAndTTL(t *testing.T) {
	committed := &store.FakeTx{}
	expired := &store.FakeTx{}
	txs := []store.Tx{committed, expired}
	db := &store.FakeDB{BeginFn: func(context.Context, *store.TxOptions) (store.Tx, error) {
		tx := txs[0]
		txs = txs[1:]
		return tx, nil
	}}
	manager := NewTransactionManager(10*time.Millisecond, 2)
	token, err := manager.Begin(context.Background(), db, "", "writer", nil, "default", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Commit(token, "", "writer", nil); err != nil {
		t.Fatal(err)
	}
	if !committed.Committed {
		t.Fatal("transaction was not committed")
	}
	token, err = manager.Begin(context.Background(), db, "", "writer", nil, "default", nil)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := manager.DB(token, "", "writer", nil, "default"); !errors.Is(err, ErrTransactionNotFound) {
		t.Fatalf("expired transaction error = %v", err)
	}
	if !expired.RolledBack {
		t.Fatal("expired transaction was not rolled back")
	}
	manager.Close()
}

func TestTransactionLifetimeOutlivesBeginRequest(t *testing.T) {
	var transactionContext context.Context
	tx := &store.FakeTx{}
	db := &store.FakeDB{BeginFn: func(ctx context.Context, _ *store.TxOptions) (store.Tx, error) {
		transactionContext = ctx
		return tx, nil
	}}
	manager := NewTransactionManager(time.Minute, 1)
	requestContext, cancelRequest := context.WithCancel(context.Background())
	token, err := manager.Begin(requestContext, db, "session", "writer", nil, "default", nil)
	if err != nil {
		t.Fatal(err)
	}
	cancelRequest()
	select {
	case <-transactionContext.Done():
		t.Fatal("transaction lifetime was tied to completed begin request")
	case <-time.After(10 * time.Millisecond):
	}
	if _, err := manager.DB(token, "session", "writer", nil, "default"); err != nil {
		t.Fatal(err)
	}
	manager.Close()
}

func TestTransactionCommitFailureRollsBackAndWrapsError(t *testing.T) {
	t.Parallel()
	cause := errors.New("commit failed")
	tx := &failingCommitTx{err: cause}
	db := &store.FakeDB{BeginFn: func(context.Context, *store.TxOptions) (store.Tx, error) {
		return tx, nil
	}}
	manager := NewTransactionManager(time.Minute, 1)
	defer manager.Close()
	token, err := manager.Begin(context.Background(), db, "", "writer", nil, "default", nil)
	if err != nil {
		t.Fatal(err)
	}
	err = manager.Commit(token, "", "writer", nil)
	if !errors.Is(err, ErrDatabase) || !errors.Is(err, cause) {
		t.Fatalf("commit error = %v", err)
	}
	if !tx.RolledBack {
		t.Fatal("failed commit did not attempt rollback")
	}
}

func TestTransactionToolsUseRequestContextAndTimeout(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name: "users", DataSource: "default",
		Role: entity.RoleAccess{entity.ActionRead: {"reader"}},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	manager := NewTransactionManager(time.Minute, 3)
	defer manager.Close()
	beginner := &store.FakeDB{BeginFn: func(ctx context.Context, _ *store.TxOptions) (store.Tx, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	tc := Context{
		Role: "reader", Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg),
		Transactions: manager, TxBeginners: map[string]store.TxBeginner{"default": beginner},
		Timeout: 5 * time.Millisecond,
	}
	_, err := (BeginTransactionTool{}).Run(context.Background(), json.RawMessage(`{}`), tc)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("begin timeout error = %v", err)
	}

	tx := &store.FakeTx{}
	immediate := &store.FakeDB{BeginFn: func(context.Context, *store.TxOptions) (store.Tx, error) {
		return tx, nil
	}}
	token, err := manager.Begin(context.Background(), immediate, "", "reader", nil, "default", nil)
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]string{"transaction": token})
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = (CommitTransactionTool{}).Run(cancelled, input, Context{
		Role: "reader", Transactions: manager,
	})
	if !errors.Is(err, context.Canceled) || tx.Committed {
		t.Fatalf("commit context error = %v, committed = %v", err, tx.Committed)
	}
	_, err = (RollbackTransactionTool{}).Run(cancelled, input, Context{
		Role: "reader", Transactions: manager,
	})
	if !errors.Is(err, context.Canceled) || tx.RolledBack {
		t.Fatalf("rollback context error = %v, rolled back = %v", err, tx.RolledBack)
	}
}

func TestTransactionDisconnectRollsBackSession(t *testing.T) {
	tx := &store.FakeTx{}
	db := &store.FakeDB{BeginFn: func(context.Context, *store.TxOptions) (store.Tx, error) {
		return tx, nil
	}}
	manager := NewTransactionManager(time.Minute, 2)
	token, err := manager.Begin(context.Background(), db, "session-a", "writer", nil, "default", nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.RollbackSession("session-a")
	if !tx.RolledBack {
		t.Fatal("session disconnect did not rollback transaction")
	}
	if _, err := manager.DB(token, "session-a", "writer", nil, "default"); !errors.Is(err, ErrTransactionNotFound) {
		t.Fatalf("disconnected transaction error = %v", err)
	}
	manager.Close()
}

func TestTransactionCapacityIsIsolatedByScope(t *testing.T) {
	t.Parallel()
	db := &store.FakeDB{BeginFn: func(context.Context, *store.TxOptions) (store.Tx, error) {
		return &store.FakeTx{}, nil
	}}
	manager := NewTransactionManager(time.Minute, 1)
	defer manager.Close()
	if _, err := manager.Begin(context.Background(), db, "", "role-a", nil, "default", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Begin(context.Background(), db, "", "role-b", nil, "default", nil); err != nil {
		t.Fatalf("one scope consumed another scope's capacity: %v", err)
	}
	if _, err := manager.Begin(context.Background(), db, "", "role-a", nil, "default", nil); !errors.Is(err, ErrTransactionCapacity) {
		t.Fatalf("same-scope capacity error = %v", err)
	}
}

func TestTransactionCacheInvalidatesOnlyAfterCommit(t *testing.T) {
	t.Parallel()
	db := &store.FakeDB{BeginFn: func(context.Context, *store.TxOptions) (store.Tx, error) {
		return &store.FakeTx{}, nil
	}}
	manager := NewTransactionManager(time.Minute, 2)
	defer manager.Close()
	cc := cache.NewTTLCache[[]map[string]any](time.Minute, 0)
	key := cache.Key{Entity: "users", SQL: "select"}
	_ = cc.Set(context.Background(), key, []map[string]any{{"id": 1}})
	token, _ := manager.Begin(context.Background(), db, "", "writer", nil, "default", nil)
	if err := manager.MarkDirty(token, "", "writer", nil, "default", "users"); err != nil {
		t.Fatal(err)
	}
	tc := Context{Role: "writer", Transactions: manager, Cache: cc}
	input, _ := json.Marshal(map[string]string{"transaction": token})
	if _, err := (RollbackTransactionTool{}).Run(context.Background(), input, tc); err != nil {
		t.Fatal(err)
	}
	if _, ok := cc.Get(context.Background(), key); !ok {
		t.Fatal("rollback polluted global cache")
	}
	token, _ = manager.Begin(context.Background(), db, "", "writer", nil, "default", nil)
	_ = manager.MarkDirty(token, "", "writer", nil, "default", "users")
	input, _ = json.Marshal(map[string]string{"transaction": token})
	if _, err := (CommitTransactionTool{}).Run(context.Background(), input, tc); err != nil {
		t.Fatal(err)
	}
	if _, ok := cc.Get(context.Background(), key); ok {
		t.Fatal("commit did not invalidate written entity")
	}
}

func TestBeginTransactionRequiresRoleAndDefaultsReadOnly(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name: "users", DataSource: "default",
		Role: entity.RoleAccess{entity.ActionRead: {"reader"}, entity.ActionCreate: {"writer"}},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	var got *store.TxOptions
	db := &store.FakeDB{BeginFn: func(_ context.Context, opts *store.TxOptions) (store.Tx, error) {
		copy := *opts
		got = &copy
		return &store.FakeTx{}, nil
	}}
	manager := NewTransactionManager(time.Minute, 2)
	defer manager.Close()
	base := Context{
		Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg), Transactions: manager,
		TxBeginners: map[string]store.TxBeginner{"default": db},
	}
	reader := base
	reader.Role = "reader"
	if _, err := (BeginTransactionTool{}).Run(context.Background(), json.RawMessage(`{"readOnly":false}`), reader); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("reader write transaction error = %v", err)
	}
	if _, err := (BeginTransactionTool{}).Run(context.Background(), json.RawMessage(`{}`), reader); err != nil {
		t.Fatal(err)
	}
	if got == nil || !got.ReadOnly {
		t.Fatalf("low-privilege transaction options = %+v", got)
	}
	nobody := base
	nobody.Role = "nobody"
	if _, err := (BeginTransactionTool{}).Run(context.Background(), json.RawMessage(`{}`), nobody); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unauthorized role error = %v", err)
	}
}
