//go:build integration

package postgres_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
)

// A scan reads each kind of metadata for all schemas at once; tables of the
// same name in two schemas must each keep their own columns, keys, foreign
// keys, cascades, triggers and privileges.
func TestPGScanKeepsSameNamedTablesApart(t *testing.T) {
	prov, cleanup := setupPG(t)
	defer cleanup()
	ctx := context.Background()
	for _, stmt := range []string{
		`CREATE TABLE items (id int PRIMARY KEY, code text NOT NULL UNIQUE, note text)`,
		`CREATE TABLE lines (id int PRIMARY KEY, item_id int REFERENCES items(id) ON DELETE CASCADE)`,
		`CREATE SCHEMA other`,
		`CREATE TABLE other.items (id int PRIMARY KEY, label text)`,
		`CREATE FUNCTION other.noop() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RETURN NEW; END$$`,
		`CREATE TRIGGER noop BEFORE INSERT ON other.items FOR EACH ROW EXECUTE FUNCTION other.noop()`,
	} {
		if _, err := prov.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	found, err := prov.Introspector().Discover(ctx, []string{"public", "other"})
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]entity.Entity{}
	for _, e := range found {
		byKey[e.Schema+"."+e.Name] = e
	}
	mine, other, lines := byKey["public.items"], byKey["other.items"], byKey["public.lines"]
	if len(mine.Attributes) != 3 || len(mine.Keys) != 2 || len(mine.Cascades) != 1 || mine.SideEffects {
		t.Fatalf("public.items = %+v", mine)
	}
	if len(other.Attributes) != 2 || len(other.Keys) != 1 || len(other.Cascades) != 0 || !other.SideEffects {
		t.Fatalf("other.items = %+v", other)
	}
	if len(lines.ForeignKeys) != 1 || lines.ForeignKeys[0].RefSchema != "public" {
		t.Fatalf("public.lines = %+v", lines)
	}
	privileges, err := prov.Introspector().(introspect.PrivilegeInspector).TablePrivileges(ctx, "other",
		[]string{"items", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := privileges["items"]; !ok || p.Select != introspect.PrivilegeGranted {
		t.Fatalf("other.items privileges = %+v", privileges)
	}
	if _, ok := privileges["missing"]; ok {
		t.Fatalf("a table the connection cannot see must be left out: %+v", privileges)
	}
}

// Every valid index is read with its access method, key parts as the
// database prints them and predicate; the unique ones are also the keys.
func TestPGScanReadsIndexes(t *testing.T) {
	prov, cleanup := setupPG(t)
	defer cleanup()
	ctx := context.Background()
	for _, stmt := range []string{
		`CREATE TABLE docs (id int PRIMARY KEY, tenant int NOT NULL, title text, tags text[], at date)`,
		`CREATE INDEX docs_tenant_title ON docs (tenant, title)`,
		`CREATE INDEX docs_tags ON docs USING gin (tags)`,
		`CREATE INDEX docs_title_hash ON docs USING hash (title)`,
		`CREATE INDEX docs_at ON docs USING brin (at)`,
		`CREATE UNIQUE INDEX docs_lower_title ON docs (tenant, lower(title)) WHERE at IS NOT NULL`,
	} {
		if _, err := prov.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	found, err := prov.Introspector().Discover(ctx, []string{"public"})
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(found, func(e entity.Entity) bool { return e.Name == "docs" })
	if i < 0 {
		t.Fatal("docs not found")
	}
	var got []string
	for _, ix := range found[i].Indexes {
		got = append(got, strings.Join([]string{ix.Name, ix.Method, strings.Join(ix.Parts, ","), ix.Where}, " "))
	}
	want := []string{
		"docs_pkey btree id ", "docs_at brin at ", "docs_lower_title btree tenant,lower(title) at IS NOT NULL",
		"docs_tags gin tags ", "docs_tenant_title btree tenant,title ", "docs_title_hash hash title ",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("indexes = %q, want %q", got, want)
	}
	if keys := found[i].Keys; len(keys) != 2 || keys[1].Reason != entity.KeyPartial {
		t.Fatalf("keys = %+v", keys)
	}
}
