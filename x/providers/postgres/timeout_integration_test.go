//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
	pgprov "github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

const sleeper = "SELECT pg_sleep(5)"

// running counts the sleeping statements still active on the server.
func running(t *testing.T, admin *pgprov.Provider) int64 {
	t.Helper()
	rows, err := admin.QueryContext(context.Background(),
		`SELECT count(*) FROM pg_stat_activity WHERE state = 'active' AND query LIKE '%pg_sleep(5)%'
		   AND pid <> pg_backend_pid()`)
	if err != nil {
		t.Fatal(err)
	}
	var n int64
	for row, err := range store.Iter(rows) {
		if err != nil {
			t.Fatal(err)
		}
		n = row["count"].(int64)
	}
	return n
}

// TestPGStatementTimeout backs limit.timeout.read and limit.timeout.write: the
// injected statement_timeout stops a read and a write on the server, and
// where it does not apply (the DSN sets statement_timeout = 0) the cancel
// request pgx sends at the request deadline does, leaving nothing running and
// nothing written.
func TestPGStatementTimeout(t *testing.T) {
	dsn, stop := startPG(t)
	defer stop()
	admin, err := pgprov.New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.ExecContext(context.Background(), `CREATE TABLE w (id int)`); err != nil {
		t.Fatal(err)
	}
	write := "INSERT INTO w SELECT 1 FROM pg_sleep(5)"
	assertNativeTimeout(t, dsn, sleeper, write)
	assertCancelledAtDeadline(t, dsn, admin, sleeper, write)
	rows, err := admin.QueryContext(context.Background(), "SELECT count(*) FROM w")
	if err != nil {
		t.Fatal(err)
	}
	for row, err := range store.Iter(rows) {
		if err != nil || row["count"].(int64) != 0 {
			t.Fatalf("timed-out writes must not land: %v %v", row, err)
		}
	}
}

// assertNativeTimeout: the injected statement_timeout stops each statement.
func assertNativeTimeout(t *testing.T, dsn string, statements ...string) {
	t.Helper()
	native, err := pgprov.Open(dsn, providerregistry.Options{Timeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	for _, statement := range statements {
		start := time.Now()
		_, err := native.ExecContext(context.Background(), statement)
		if !errors.Is(err, store.ErrStatementTimeout) || time.Since(start) > 3*time.Second {
			t.Fatalf("statement_timeout: %s = %v after %v", statement, err, time.Since(start))
		}
	}
}

// assertCancelledAtDeadline: with statement_timeout off, a statement whose
// request deadline passes no longer runs on the server.
func assertCancelledAtDeadline(t *testing.T, dsn string, admin *pgprov.Provider, statements ...string) {
	t.Helper()
	overridden, err := pgprov.Open(dsn+"&statement_timeout=0", providerregistry.Options{Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer overridden.Close()
	for _, statement := range statements {
		deadline, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		_, err := overridden.ExecContext(deadline, statement)
		cancel()
		if err == nil {
			t.Fatalf("%s outlived its deadline", statement)
		}
		time.Sleep(time.Second)
		if n := running(t, admin); n != 0 {
			t.Fatalf("%s: %d statements still run after the deadline (no cancel request)", statement, n)
		}
	}
}
