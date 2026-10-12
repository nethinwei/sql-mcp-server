//go:build integration

package oceanbase_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
	"github.com/nethinwei/sql-mcp-server/x/providers/oceanbase"
)

// TestOBStatementTimeout backs limit.timeout.read, limit.timeout.write and
// limit.unkeyedWrite: the injected ob_query_timeout stops a read and a write
// on the server as statement timeouts (the write does not land), and the
// injected sql_safe_updates refuses an unkeyed write.
func TestOBStatementTimeout(t *testing.T) {
	_, dsn := startOBContainer(t)
	prov := connectOBWithRetry(t, dsn)
	defer prov.Close()
	seedOBSchema(t, prov)
	ctx := context.Background()
	if _, err := prov.ExecContext(ctx, "CREATE TABLE test.w (id int PRIMARY KEY, v int)"); err != nil {
		t.Fatal(err)
	}
	timed, err := oceanbase.Open(dsn, providerregistry.Options{Timeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer timed.Close()
	for _, statement := range []string{"SELECT SLEEP(5)", "INSERT INTO test.w SELECT 1, SLEEP(5)"} {
		start := time.Now()
		_, err := timed.ExecContext(ctx, statement)
		if !errors.Is(err, store.ErrStatementTimeout) || time.Since(start) > 3*time.Second {
			t.Fatalf("ob_query_timeout: %s = %v after %v", statement, err, time.Since(start))
		}
	}
	rows, err := prov.QueryContext(ctx, "SELECT COUNT(*) AS n FROM test.w")
	if err != nil {
		t.Fatal(err)
	}
	for row, err := range store.Iter(rows) {
		if err != nil || row["n"] != int64(0) {
			t.Fatalf("a timed-out write must not land: %v %v", row, err)
		}
	}
	if _, err := timed.ExecContext(ctx, "UPDATE test.users SET email = email"); err == nil ||
		!strings.Contains(err.Error(), "1175") {
		t.Fatalf("sql_safe_updates must refuse an unkeyed update: %v", err)
	}
}
