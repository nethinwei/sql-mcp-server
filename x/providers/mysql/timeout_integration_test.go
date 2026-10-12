//go:build integration

package mysql_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
	"github.com/nethinwei/sql-mcp-server/x/providers/mysql"
)

// longRead runs far past any test timeout: a triple cross join of the
// server's own column catalog.
const longRead = "SELECT COUNT(*) FROM information_schema.columns a, information_schema.columns b, " +
	"information_schema.columns c"

// TestMySQLStatementTimeout backs limit.timeout.read and limit.unkeyedWrite:
// the injected max_execution_time stops a read on the server as a statement
// timeout, and the injected sql_safe_updates refuses an unkeyed write.
func TestMySQLStatementTimeout(t *testing.T) {
	dsn, stop := startMySQL(t)
	defer stop()
	ctx := context.Background()
	seed := connectMySQLWithRetry(t, dsn)
	defer seed.Close()
	if _, err := seed.ExecContext(ctx, "CREATE TABLE users (id int PRIMARY KEY, email text)"); err != nil {
		t.Fatal(err)
	}
	timed, err := mysql.Open(dsn, providerregistry.Options{Timeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer timed.Close()
	start := time.Now()
	rows, err := timed.QueryContext(ctx, longRead)
	if err == nil {
		for _, err = range store.Iter(rows) {
			if err != nil {
				break
			}
		}
	}
	if !errors.Is(err, store.ErrStatementTimeout) || time.Since(start) > 5*time.Second {
		t.Fatalf("max_execution_time: %v after %v", err, time.Since(start))
	}
	if _, err := timed.ExecContext(ctx, "UPDATE users SET email = email"); err == nil ||
		!strings.Contains(err.Error(), "1175") {
		t.Fatalf("sql_safe_updates must refuse an unkeyed update: %v", err)
	}
}
