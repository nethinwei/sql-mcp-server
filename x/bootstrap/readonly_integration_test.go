//go:build integration

package bootstrap

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"
	"time"

	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
	"github.com/nethinwei/sql-mcp-server/x/providers/mysql"
)

// An escape hatch connection through a transaction pooler cannot stop a
// write past its deadline (no KILL QUERY there). Its write timeout is an
// issue for an account that can write, and none for one that cannot.
func TestMySQLReadOnlyEscapeAccountNeedsNoWriteTimeout(t *testing.T) {
	ctx := context.Background()
	container, err := tcmysql.Run(ctx, "mysql:8", tcmysql.WithDatabase("test"),
		tcmysql.WithUsername("test"), tcmysql.WithPassword("test"))
	if err != nil {
		t.Fatalf("start mysql container: %v", err)
	}
	defer func() { _ = container.Terminate(context.Background()) }()
	dsn, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	as := func(user string) string { return strings.Replace(dsn, "test:test@", user+"@", 1) }
	root := openWithRetry(t, as("root:test"))
	defer func() { _ = root.Close() }()
	for _, stmt := range []string{
		`CREATE TABLE test.notes (id int PRIMARY KEY)`,
		`CREATE USER 'ro'@'%' IDENTIFIED BY 'ro'`, `GRANT SELECT, SHOW VIEW ON test.* TO 'ro'@'%'`,
		`CREATE USER 'rw'@'%' IDENTIFIED BY 'rw'`, `GRANT SELECT, UPDATE ON test.* TO 'rw'@'%'`,
	} {
		if _, err := root.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	for account, wantIssue := range map[string]bool{"ro:ro": false, "rw:rw": true} {
		escape, err := mysql.Open(as(account), providerregistry.Options{
			Timeout: 10 * time.Second, Pooler: providerregistry.PoolerTransaction,
		})
		if err != nil {
			t.Fatal(err)
		}
		cfg := &config.Config{
			Databases: map[string]config.DatabaseConfig{"main": {
				Driver: "mysql", Connections: config.Connections{"escape": {DSN: "x", Pooler: "transaction"}},
				SQLEscape: config.SQLEscapeConfig{Connection: "escape"},
			}},
			Tools: config.ToolFlags{ExecuteSQL: true},
		}
		conns := Connections{"main": {"escape": escape}}
		issues := issueKeys(NativeLimitIssues(cfg, conns, readOnlyEscapes(cfg, conns)))
		got := slices.Contains(issues, "escape limit.timeout.write the SQL escape hatch")
		_ = escape.Close()
		if got != wantIssue {
			t.Errorf("%s: write timeout issue %v, want %v (issues %q)", account, got, wantIssue, issues)
		}
	}
}

func openWithRetry(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	for range 40 {
		if err = db.Ping(); err == nil {
			return db
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("connect mysql: %v", err)
	return nil
}
