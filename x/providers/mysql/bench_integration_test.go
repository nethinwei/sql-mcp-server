//go:build integration

package mysql_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/introspect"
)

// benchTables creates pairs of tables: a parent with a primary and a unique
// key, and a child with a cascading foreign key to it.
func benchTables(b *testing.B, exec func(context.Context, string, ...any) error, pairs int) []string {
	b.Helper()
	var names []string
	for n := range pairs {
		for _, stmt := range []string{
			fmt.Sprintf("CREATE TABLE p%d (id integer PRIMARY KEY, code varchar(20) NOT NULL UNIQUE, note text)", n),
			fmt.Sprintf("CREATE TABLE c%d (id integer PRIMARY KEY, v integer, "+
				"pid integer REFERENCES p%d(id) ON DELETE CASCADE)", n, n),
		} {
			if err := exec(context.Background(), stmt); err != nil {
				b.Fatal(err)
			}
		}
		names = append(names, fmt.Sprintf("p%d", n), fmt.Sprintf("c%d", n))
	}
	return names
}

// BenchmarkMySQLDiscover600Tables scans a schema of 600 tables: the number of
// round trips must not grow with the tables (make bench-integration).
func BenchmarkMySQLDiscover600Tables(b *testing.B) {
	prov, cleanup := setupMySQL(b)
	defer cleanup()
	benchTables(b, func(ctx context.Context, q string, args ...any) error {
		_, err := prov.ExecContext(ctx, q, args...)
		return err
	}, 300)
	ctx := context.Background()
	for b.Loop() {
		if _, err := prov.Introspector().Discover(ctx, []string{"test"}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkMySQLTablePrivileges600Tables probes the privileges on 600 tables
// of one schema, as assembly does for configured entities.
func BenchmarkMySQLTablePrivileges600Tables(b *testing.B) {
	prov, cleanup := setupMySQL(b)
	defer cleanup()
	names := benchTables(b, func(ctx context.Context, q string, args ...any) error {
		_, err := prov.ExecContext(ctx, q, args...)
		return err
	}, 300)
	inspect := prov.Introspector().(introspect.PrivilegeInspector)
	ctx := context.Background()
	for b.Loop() {
		if _, err := inspect.TablePrivileges(ctx, "test", names); err != nil {
			b.Fatal(err)
		}
	}
}
