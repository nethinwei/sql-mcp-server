//go:build integration

package mysql_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/x/providers/mysql"
)

// setupMySQLTwoDatabases creates items in the test database (with a
// cascading child) and in a second database (with a trigger) the test account
// can read, and connects as that account.
func setupMySQLTwoDatabases(t *testing.T) (*mysql.Provider, func()) {
	t.Helper()
	ctx := context.Background()
	container, err := tcmysql.Run(ctx, "mysql:8", tcmysql.WithDatabase("test"),
		tcmysql.WithUsername("test"), tcmysql.WithPassword("test"))
	if err != nil {
		t.Fatalf("start mysql container: %v", err)
	}
	dsn, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	root := connectMySQLWithRetry(t, strings.Replace(dsn, "test:test@", "root:test@", 1))
	defer func() { _ = root.Close() }()
	for _, stmt := range []string{
		`CREATE TABLE test.items (id int PRIMARY KEY, code varchar(10) NOT NULL UNIQUE, note text)`,
		`CREATE TABLE test.lines (id int PRIMARY KEY, item_id int,
		   FOREIGN KEY (item_id) REFERENCES test.items(id) ON DELETE CASCADE)`,
		`CREATE DATABASE other`,
		`CREATE TABLE other.items (id int PRIMARY KEY, label text)`,
		`CREATE TRIGGER other.noop BEFORE INSERT ON other.items FOR EACH ROW SET NEW.label = NEW.label`,
		`GRANT SELECT, TRIGGER ON other.* TO 'test'@'%'`,
		`CREATE TABLE test.docs (id int PRIMARY KEY, tenant int NOT NULL, title varchar(200), body text,
		   pos point NOT NULL SRID 0, KEY docs_tenant_title (tenant, title(20)), UNIQUE KEY docs_title (title),
		   FULLTEXT KEY docs_body (body), SPATIAL KEY docs_pos (pos))`,
	} {
		if _, err := root.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	prov := connectMySQLWithRetry(t, dsn)
	return prov, func() {
		_ = prov.Close()
		_ = container.Terminate(context.Background())
	}
}

// A scan reads each kind of metadata for all databases at once; tables of
// the same name in two databases must each keep their own columns, keys,
// foreign keys, cascades, triggers and privileges.
func TestMySQLScanKeepsSameNamedTablesApart(t *testing.T) {
	prov, cleanup := setupMySQLTwoDatabases(t)
	defer cleanup()
	ctx := context.Background()
	found, err := prov.Introspector().Discover(ctx, []string{"test", "other"})
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]entity.Entity{}
	for _, e := range found {
		byKey[e.Schema+"."+e.Name] = e
	}
	mine, other, lines := byKey["test.items"], byKey["other.items"], byKey["test.lines"]
	if len(mine.Attributes) != 3 || len(mine.Keys) != 2 || len(mine.Cascades) != 1 || mine.SideEffects {
		t.Fatalf("test.items = %+v", mine)
	}
	if len(other.Attributes) != 2 || len(other.Keys) != 1 || len(other.Cascades) != 0 || !other.SideEffects {
		t.Fatalf("other.items = %+v", other)
	}
	if len(lines.ForeignKeys) != 1 || lines.ForeignKeys[0].RefSchema != "test" {
		t.Fatalf("test.lines = %+v", lines)
	}
	privileges, err := prov.Introspector().(introspect.PrivilegeInspector).TablePrivileges(ctx, "other",
		[]string{"items"})
	if err != nil {
		t.Fatal(err)
	}
	if p := privileges["items"]; p.Select != introspect.PrivilegeGranted || p.Insert == introspect.PrivilegeGranted {
		t.Fatalf("other.items privileges = %+v", p)
	}
}

// describeIndexes renders each index of e as "name method [unique] parts".
func describeIndexes(e entity.Entity) []string {
	var out []string
	for _, ix := range e.Indexes {
		d := ix.Name + " " + ix.Method
		if ix.Unique {
			d += " unique"
		}
		out = append(out, d+" "+strings.Join(ix.Parts, ","))
	}
	return out
}

// Every index is read with its structure and key parts; the unique ones are
// also the keys.
func TestMySQLScanReadsIndexes(t *testing.T) {
	prov, cleanup := setupMySQLTwoDatabases(t)
	defer cleanup()
	found, err := prov.Introspector().Discover(context.Background(), []string{"test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range found {
		if e.Name != "docs" {
			continue
		}
		want := []string{
			"PRIMARY btree unique id", "docs_body fulltext body", "docs_pos spatial pos",
			"docs_tenant_title btree tenant,title(20)", "docs_title btree unique title",
		}
		if got := describeIndexes(e); !slices.Equal(got, want) {
			t.Fatalf("indexes = %q, want %q", got, want)
		}
		if len(e.Keys) != 2 || e.Keys[1].Name != "docs_title" || e.Keys[1].Reason != entity.KeyNullable {
			t.Fatalf("keys = %+v", e.Keys)
		}
		return
	}
	t.Fatal("docs not found")
}
