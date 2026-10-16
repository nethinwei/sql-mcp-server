package tool

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/codegen"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/core/store/storetest"
)

// scriptedTx runs statements with query and exec.
type scriptedTx struct {
	storetest.FakeTx
	query func() (store.Rows, error)
	exec  func() error
}

func (t *scriptedTx) QueryContext(context.Context, string, ...any) (store.Rows, error) {
	return t.query()
}
func (t *scriptedTx) ExecContext(context.Context, string, ...any) (store.Result, error) {
	return store.Result{}, t.exec()
}

func beginScripted(t *testing.T, m *TransactionManager, tx *scriptedTx) (string, store.DB, Caller) {
	t.Helper()
	db := &storetest.FakeDB{BeginFn: func(context.Context, *store.TxOptions) (store.Tx, error) { return tx, nil }}
	caller := Caller{Session: "s", Role: "writer"}
	token, err := m.Begin(bg, db, caller, "primary", nil)
	if err != nil {
		t.Fatal(err)
	}
	txDB, err := m.DB(bg, token, caller, "primary", nil)
	if err != nil {
		t.Fatal(err)
	}
	return token, txDB, caller
}

// failingStatements run a statement that fails: on sending it, or while its
// rows are read.
func failingStatements() map[string]func(store.DB) error {
	return map[string]func(store.DB) error{
		"exec": func(db store.DB) error {
			_, err := db.ExecContext(bg, "UPDATE")
			return err
		},
		"query": func(db store.DB) error {
			_, err := db.QueryContext(bg, "SELECT")
			return err
		},
		"rows": func(db store.DB) error {
			rows, err := db.QueryContext(bg, "SELECT")
			if err != nil {
				return err
			}
			for _, err := range store.IterNamed(rows, nil) {
				if err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// A statement failing inside a transaction rolls it back at once, however it
// fails: on sending it, or while its rows are read. Its error says so, and
// the token then tells the transaction was rolled back.
func TestFailedStatementEndsTheTransaction(t *testing.T) {
	t.Parallel()
	broken := errors.New("column w does not exist")
	for name, run := range failingStatements() {
		m := NewTransactionManager(time.Minute, 4)
		tx := &scriptedTx{
			exec: func() error { return broken },
			query: func() (store.Rows, error) {
				if name == "rows" {
					rows := storetest.NewFakeRows([]string{"id"})
					rows.SetErr(broken)
					return rows, nil
				}
				return nil, broken
			},
		}
		token, db, caller := beginScripted(t, m, tx)
		err := run(db)
		if !errors.Is(err, broken) || !endedTransaction(err) {
			t.Fatalf("%s: err = %v", name, err)
		}
		var ended *TransactionEndedError
		if _, err := m.DB(bg, token, caller, "primary", nil); !errors.As(err, &ended) ||
			ended.Outcome != OutcomeRolledBack || !tx.RolledBack {
			t.Fatalf("%s: after the failure: %v (rolled back %v)", name, err, tx.RolledBack)
		}
		m.Close()
	}
}

// A transaction no statement uses for its idle timeout is rolled back; one
// in use, or running a statement longer than it, is not.
func TestIdleTransactionIsRolledBack(t *testing.T) {
	t.Parallel()
	m := NewTransactionManager(time.Minute, 4)
	defer m.Close()
	m.SetIdleTimeout(60 * time.Millisecond)
	release := make(chan struct{})
	slow := &scriptedTx{exec: func() error { <-release; return nil }}
	busy := &scriptedTx{exec: func() error { return nil }}
	idle := &scriptedTx{}
	slowToken, slowDB, caller := beginScripted(t, m, slow)
	busyToken, busyDB, _ := beginScripted(t, m, busy)
	idleToken, _, _ := beginScripted(t, m, idle)
	go func() { _, _ = slowDB.ExecContext(bg, "UPDATE") }()
	for range 10 {
		if _, err := busyDB.ExecContext(bg, "UPDATE"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	var ended *TransactionEndedError
	if _, err := m.DB(bg, idleToken, caller, "primary", nil); !errors.As(err, &ended) ||
		ended.Outcome != OutcomeExpired || !idle.RolledBack {
		t.Fatalf("the idle transaction: %v", err)
	}
	for name, token := range map[string]string{"busy": busyToken, "slow": slowToken} {
		if _, err := m.DB(bg, token, caller, "primary", nil); err != nil {
			t.Fatalf("the %s transaction ended: %v", name, err)
		}
	}
	close(release)
}

// A statement whose table or column is gone makes its entity unavailable,
// raises the drift signal, and, inside a transaction, tells that the
// transaction was rolled back.
func TestSchemaChangeUnderAStatement(t *testing.T) {
	t.Parallel()
	users := entity.Resolved{Entity: entity.Entity{Name: "users"}}
	missing := fmt.Errorf("%w: pq: column", store.ErrMissingColumn)
	drift := &SchemaDrift{}
	gone := func(context.Context, string, ...any) (store.Rows, error) { return nil, missing }
	st := statement{
		tc: Context{
			Services: &Services{Drift: drift},
			DB:       &storetest.FakeDB{QueryFn: gone},
		},
		res: users, grant: grant{rbac.Decision{Allowed: true}}, sql: gated{Compiled: codegen.Compiled{SQL: "SELECT"}},
	}
	_, err := st.query(bg)
	d, ok := DenialFor(err, "d")
	if !ok || d.Code != CodeEntityUnavailable || len(d.Hints) == 0 || !drift.Take() {
		t.Fatalf("denial = %+v, %v", d, ok)
	}
	if d.Constraints["transaction"] != nil {
		t.Fatalf("no transaction was rolled back: %+v", d)
	}
	rolledBack := &TransactionRolledBackError{Err: st.classify(&statementFailed{err: missing})}
	if d, _ := DenialFor(rolledBack, "d"); d.Code != CodeEntityUnavailable ||
		d.Constraints["transaction"] != OutcomeRolledBack {
		t.Fatalf("in a transaction: %+v", d)
	}
}

// A statement its cached plan made stale runs again outside a transaction;
// inside one, which the refusal ended, it does not.
func TestStalePlanIsRetriedOutsideTransactions(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	do := func() (int, error) {
		if calls.Add(1) == 1 {
			return 0, fmt.Errorf("%w: cached plan", store.ErrStalePlan)
		}
		return 1, nil
	}
	if got, err := retryStalePlan(Context{}, do); err != nil || got != 1 || calls.Load() != 2 {
		t.Fatalf("outside a transaction: %d, %v after %d calls", got, err, calls.Load())
	}
	calls.Store(0)
	if _, err := retryStalePlan(Context{Transaction: "t"}, do); !errors.Is(err, store.ErrStalePlan) ||
		calls.Load() != 1 {
		t.Fatalf("inside a transaction: %v after %d calls", err, calls.Load())
	}
}

// A schema change the cost gate's EXPLAIN meets, before any statement, is
// reported for the call's entity alike.
func TestSchemaChangeBeforeAStatement(t *testing.T) {
	t.Parallel()
	drift := &SchemaDrift{}
	tc := Context{Services: &Services{Drift: drift}}
	gone := fmt.Errorf("explain: %w", store.ErrMissingRelation)
	err := schemaChangeOfCall(tc, "read_records", []byte(`{"entity":"shop.orders"}`), gone)
	if d, ok := DenialFor(err, "d"); !ok || d.Code != CodeEntityUnavailable ||
		!strings.Contains(d.Reason, "shop.orders") || !drift.Take() {
		t.Fatalf("denial = %+v, %v", d, ok)
	}
	other := errors.New("boom")
	if schemaChangeOfCall(tc, "read_records", nil, other) != other || drift.Pending() {
		t.Fatal("other errors pass through")
	}
}
