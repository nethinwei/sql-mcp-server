//go:build integration

package mysql_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"

	"github.com/nethinwei/sql-mcp-server/core/introspect"
)

func TestMySQLPrivileges(t *testing.T) {
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
	root := connectMySQLWithRetry(t, strings.Replace(dsn, "test:test@", "root:test@", 1))
	defer func() { _ = root.Close() }()
	for _, stmt := range []string{
		`CREATE TABLE test.notes (id int PRIMARY KEY, body text, secret text)`,
		`CREATE USER 'ro'@'%' IDENTIFIED BY 'ro'`,
		`GRANT SELECT ON test.notes TO 'ro'@'%'`,
		`GRANT UPDATE (body) ON test.notes TO 'ro'@'%'`,
	} {
		if _, err := root.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	ro := connectMySQLWithRetry(t, strings.Replace(dsn, "test:test@", "ro:ro@", 1))
	defer func() { _ = ro.Close() }()
	inspect := ro.Introspector().(introspect.PrivilegeInspector)
	p, err := inspect.TablePrivileges(ctx, "", "notes")
	if err != nil {
		t.Fatal(err)
	}
	if p.Select != introspect.PrivilegeGranted || p.Insert != introspect.PrivilegeDenied ||
		p.Update != introspect.PrivilegeDenied || !slices.Equal(p.Columns["update"], []string{"body"}) {
		t.Fatalf("privileges = %+v", p)
	}
	if readOnly, err := inspect.ReadOnly(ctx); err != nil || readOnly {
		t.Fatalf("read only = %v, %v", readOnly, err)
	}
}
