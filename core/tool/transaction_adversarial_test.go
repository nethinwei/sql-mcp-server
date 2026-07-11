package tool

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/store"
)

type transactionScopeTest struct {
	name       string
	session    string
	role       string
	subject    map[string]any
	datasource string
	want       error
}

var adversarialTransactionScopeTests = []transactionScopeTest{
	{"same scope", "session-a", "writer", map[string]any{"tenant_id": "a"}, "primary", nil},
	{"cross session", "session-b", "writer", map[string]any{"tenant_id": "a"}, "primary", ErrTransactionScope},
	{"cross role", "session-a", "reader", map[string]any{"tenant_id": "a"}, "primary", ErrTransactionScope},
	{"cross subject", "session-a", "writer", map[string]any{"tenant_id": "b"}, "primary", ErrTransactionScope},
	{"cross datasource", "session-a", "writer", map[string]any{"tenant_id": "a"}, "replica", ErrTransactionScope},
}

func TestAdversarialTransactionScopeAndTerminalReuse(t *testing.T) {
	for _, terminal := range []string{"commit", "rollback"} {
		t.Run(terminal, func(t *testing.T) {
			testAdversarialTransactionScopeAndTerminalReuse(t, terminal)
		})
	}
}

func testAdversarialTransactionScopeAndTerminalReuse(t *testing.T, terminal string) {
	manager, _, db := adversarialTransactionManager()
	defer manager.Close()
	subject := map[string]any{"tenant_id": "a"}
	token, err := manager.Begin(context.Background(), db, "session-a", "writer", subject, "primary", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range adversarialTransactionScopeTests {
		t.Run(test.name, func(t *testing.T) {
			_, err := manager.DB(token, test.session, test.role, test.subject, test.datasource)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
	if terminal == "commit" {
		err = manager.Commit(token, "session-a", "writer", subject)
	} else {
		err = manager.Rollback(token, "session-a", "writer", subject)
	}
	if err != nil {
		t.Fatal(err)
	}
	assertTerminalTransactionTokenRejected(t, manager, token, subject)
}

func assertTerminalTransactionTokenRejected(
	t *testing.T, manager *TransactionManager, token string, subject map[string]any,
) {
	if _, err := manager.DB(token, "session-a", "writer", subject, "primary"); !errors.Is(err, ErrTransactionNotFound) {
		t.Fatalf("terminal token reuse error = %v", err)
	}
	if err := manager.Commit(token, "session-a", "writer", subject); !errors.Is(err, ErrTransactionNotFound) {
		t.Fatalf("second terminal operation error = %v", err)
	}
}

func FuzzTransactionStateMachine(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 5, 7})
	f.Add([]byte{0, 4, 6, 7})
	f.Add([]byte{1, 2, 3})

	f.Fuzz(func(t *testing.T, operations []byte) {
		fuzzTransactionStateMachine(t, operations)
	})
}

func fuzzTransactionStateMachine(t *testing.T, operations []byte) {
	manager, tx, db := adversarialTransactionManager()
	subject := map[string]any{"tenant_id": "a"}
	token, err := manager.Begin(context.Background(), db, "session-a", "writer", subject, "primary", nil)
	if err != nil {
		t.Fatal(err)
	}
	terminal := false
	for _, operation := range operations {
		err, terminal = runFuzzTransactionOperation(manager, token, subject, operation, terminal)
		assertFuzzTransactionOperation(t, operation, terminal, err)
	}
	assertFuzzTransactionManagerClosed(t, manager, tx)
}

func adversarialTransactionManager() (*TransactionManager, *store.FakeTx, *store.FakeDB) {
	tx := &store.FakeTx{}
	db := &store.FakeDB{BeginFn: func(context.Context, *store.TxOptions) (store.Tx, error) {
		return tx, nil
	}}
	return NewTransactionManager(time.Minute, 1), tx, db
}

func runFuzzTransactionOperation(
	manager *TransactionManager, token string, subject map[string]any, operation byte, terminal bool,
) (error, bool) {
	var err error
	switch operation % 8 {
	case 0, 7:
		_, err = manager.DB(token, "session-a", "writer", subject, "primary")
	case 1:
		_, err = manager.DB(token, "session-b", "writer", subject, "primary")
	case 2:
		_, err = manager.DB(token, "session-a", "reader", subject, "primary")
	case 3:
		_, err = manager.DB(token, "session-a", "writer", map[string]any{"tenant_id": "b"}, "primary")
	case 4:
		_, err = manager.DB(token, "session-a", "writer", subject, "replica")
	case 5:
		err = manager.Commit(token, "session-a", "writer", subject)
	case 6:
		err = manager.Rollback(token, "session-a", "writer", subject)
	}
	return err, terminal || (operation%8 == 5 || operation%8 == 6) && err == nil
}

func assertFuzzTransactionOperation(t *testing.T, operation byte, terminal bool, err error) {
	if terminal {
		if err != nil && !errors.Is(err, ErrTransactionNotFound) {
			t.Fatalf("terminal state returned %v", err)
		}
		return
	}
	if operation%8 >= 1 && operation%8 <= 4 {
		if !errors.Is(err, ErrTransactionScope) {
			t.Fatalf("cross-scope operation returned %v", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("valid operation returned %v", err)
	}
}

func assertFuzzTransactionManagerClosed(t *testing.T, manager *TransactionManager, tx *store.FakeTx) {
	manager.Close()
	manager.mu.Lock()
	open := len(manager.handles)
	manager.mu.Unlock()
	if open != 0 {
		t.Fatalf("manager retained %d open transactions", open)
	}
	if !tx.Committed && !tx.RolledBack {
		t.Fatal("transaction remained open after manager close")
	}
}
