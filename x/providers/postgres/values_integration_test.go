//go:build integration

package postgres_test

import (
	"context"
	"slices"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/entity"
)

// JSON, JSONB and XML values are their text, as clients expect; bytea values
// stay bytes.
func TestPGReturnsTextValuesAsText(t *testing.T) {
	prov, cleanup := setupPG(t)
	defer cleanup()
	ctx := context.Background()
	rows, err := prov.QueryContext(ctx,
		`SELECT '{"k": 1}'::jsonb, '[1]'::json, '<a/>'::xml, '\x0102'::bytea`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		t.Fatal("no row")
	}
	values := make([]any, 4)
	pointers := []any{&values[0], &values[1], &values[2], &values[3]}
	if err := rows.Scan(pointers...); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{`{"k": 1}`, `[1]`, `<a/>`} {
		if got, ok := values[i].(string); !ok || got != want {
			t.Errorf("column %d = %#v, want the text %q", i+1, values[i], want)
		}
	}
	if b, ok := values[3].([]byte); !ok || string(b) != "\x01\x02" {
		t.Errorf("bytea = %#v, want its bytes", values[3])
	}
}

// A domain's default and NOT NULL are its columns': the database fills a
// column of a defaulted domain, and does not take NULL for one of a NOT NULL
// domain on insert. But that NOT NULL does not hold for every stored row (an
// outer join's NULL passes it), so a unique key on it identifies no row. A
// comment stays on its column when an earlier one is dropped.
func TestPGScansDomainsAndComments(t *testing.T) {
	prov, cleanup := setupPG(t)
	defer cleanup()
	ctx := context.Background()
	for _, stmt := range []string{
		`CREATE DOMAIN nni AS integer NOT NULL DEFAULT 7`,
		`CREATE TABLE dom (gone integer, a nni, b text)`,
		`CREATE UNIQUE INDEX dom_a ON dom (a)`,
		`COMMENT ON COLUMN dom.b IS 'the b column'`,
		`ALTER TABLE dom DROP COLUMN gone`,
	} {
		if _, err := prov.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	found, err := prov.Introspector().Discover(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var dom entity.Entity
	for _, e := range found {
		if e.Name == "dom" {
			dom = e
		}
	}
	attrs := map[string]entity.Attribute{}
	for _, a := range dom.Attributes {
		attrs[a.Name] = a
	}
	if d := attrs["a"].Domain; d.Nullable || !d.Default || d.Required() {
		t.Errorf("a (a NOT NULL domain with a default) = %+v, want not nullable, defaulted, not required", d)
	}
	if got := attrs["b"].Description; got != "the b column" {
		t.Errorf("b's comment = %q, want %q", got, "the b column")
	}
	i := slices.IndexFunc(dom.Keys, func(k entity.Key) bool { return k.Name == "dom_a" })
	if i < 0 || dom.Keys[i].Reason != entity.KeyNullable {
		t.Errorf("dom keys = %+v, want dom_a nullable (a domain's NOT NULL does not hold for stored rows)", dom.Keys)
	}
}

// A NOT VALID NOT NULL constraint (PostgreSQL 18) sets attnotnull over rows it
// has not checked: a unique key on its column identifies no row.
func TestPGNotValidNotNullMakesNoKey(t *testing.T) {
	prov, cleanup := setupPG(t)
	defer cleanup()
	ctx := context.Background()
	var version int
	if err := prov.DB().QueryRowContext(ctx, `SELECT current_setting('server_version_num')::int`).
		Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version < 180000 {
		t.Skip("NOT VALID NOT NULL constraints are PostgreSQL 18's")
	}
	for _, stmt := range []string{
		`CREATE TABLE nv (k integer)`, `INSERT INTO nv VALUES (NULL), (NULL)`,
		`CREATE UNIQUE INDEX nv_k ON nv (k)`, `ALTER TABLE nv ADD CONSTRAINT nv_k_nn NOT NULL k NOT VALID`,
	} {
		if _, err := prov.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	found, err := prov.Introspector().Discover(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(found, func(e entity.Entity) bool { return e.Name == "nv" })
	if i < 0 || len(found[i].Keys) != 1 || found[i].Keys[0].Reason != entity.KeyNullable {
		t.Fatalf("nv = %+v, want nv_k nullable", found)
	}
}
