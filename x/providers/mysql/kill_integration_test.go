//go:build integration

package mysql_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
	"github.com/nethinwei/sql-mcp-server/x/providers/mysql"
)

// TestMySQLWriteTimeoutKillsTheStatement backs limit.timeout.write: a write
// still running at its context's deadline is stopped on the server (KILL
// QUERY), rolled back, and its row locks released, whether it ran directly or
// prepared; without that the server would run it on past the deadline.
func TestMySQLWriteTimeoutKillsTheStatement(t *testing.T) {
	dsn, stop := startMySQL(t)
	defer stop()
	ctx := context.Background()
	seed := connectMySQLWithRetry(t, dsn)
	defer seed.Close()
	seedAccounts(t, seed)
	timed, err := mysql.Open(dsn, providerregistry.Options{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer timed.Close()
	if claim := timed.Profile().Claim(dialect.TimeoutWrite); claim.Strength != dialect.Enforced {
		t.Fatalf("limit.timeout.write = %+v, want enforced", claim)
	}
	other, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	for name, exec := range slowUpdates(timed) {
		deadline, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		start := time.Now()
		err := exec(deadline)
		cancel()
		if err == nil || time.Since(start) > 5*time.Second {
			t.Fatalf("%s: the slow update returned %v after %v", name, err, time.Since(start))
		}
		assertRowFree(t, other, name)
	}
	time.Sleep(11 * time.Second) // past the SLEEP: an update left running would have committed
	var balance int
	if err := other.QueryRowContext(ctx, "SELECT balance FROM accounts WHERE id = 1").Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != 100 {
		t.Fatalf("balance = %d: a timed-out update ran on and committed", balance)
	}
}

// slowUpdates run an update that sleeps 10 seconds, directly and prepared.
func slowUpdates(p *mysql.Provider) map[string]func(context.Context) error {
	const slow = "UPDATE accounts SET balance = balance + 1 WHERE id = 1 AND SLEEP(10) = 0"
	return map[string]func(context.Context) error{
		"direct": func(ctx context.Context) error {
			_, err := p.ExecContext(ctx, slow)
			return err
		},
		"prepared": func(ctx context.Context) error {
			s, err := p.PrepareContext(context.Background(), slow)
			if err != nil {
				return err
			}
			defer s.Close()
			_, err = s.ExecContext(ctx)
			return err
		},
	}
}

// seedAccounts creates the account row the tests update.
func seedAccounts(t *testing.T, seed *mysql.Provider) {
	t.Helper()
	for _, q := range []string{
		"CREATE TABLE accounts (id int PRIMARY KEY, balance int NOT NULL)", "INSERT INTO accounts VALUES (1, 100)",
	} {
		if _, err := seed.ExecContext(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
}

// assertRowFree takes the row's lock without waiting: the stopped update
// released it.
func assertRowFree(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := lockRow(db)
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: the row is still locked after the deadline: %v", name, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func lockRow(db *sql.DB) error {
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, "SELECT balance FROM accounts WHERE id = 1 FOR UPDATE NOWAIT")
	return err
}

// TestMySQLLockWaitingWriteIsStopped: a write waiting on a lock at its
// deadline is stopped too, so it does not apply once the lock is released.
func TestMySQLLockWaitingWriteIsStopped(t *testing.T) {
	dsn, stop := startMySQL(t)
	defer stop()
	ctx := context.Background()
	seed := connectMySQLWithRetry(t, dsn)
	defer seed.Close()
	seedAccounts(t, seed)
	holder, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	tx, err := holder.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "SELECT balance FROM accounts WHERE id = 1 FOR UPDATE"); err != nil {
		t.Fatal(err)
	}
	timed, err := mysql.Open(dsn, providerregistry.Options{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer timed.Close()
	deadline, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	_, err = timed.ExecContext(deadline, "UPDATE accounts SET balance = 0 WHERE id = 1")
	cancel()
	if err == nil {
		t.Fatal("the update waiting on the lock succeeded")
	}
	time.Sleep(time.Second) // the KILL QUERY lands
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second) // an update left waiting would apply now
	var balance int
	if err := holder.QueryRowContext(ctx, "SELECT balance FROM accounts WHERE id = 1").Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != 100 {
		t.Fatalf("balance = %d: the timed-out update applied after the lock was released", balance)
	}
}
