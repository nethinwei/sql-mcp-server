//go:build integration

package postgres_test

import (
	"context"
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
// column of a defaulted domain, and a unique key on a NOT NULL domain
// identifies rows. A comment stays on its column when an earlier one is
// dropped.
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
	for _, k := range dom.Keys {
		if k.Name == "dom_a" && k.Reason != "" {
			t.Errorf("dom_a reason = %q, want a key (its column's domain is NOT NULL)", k.Reason)
		}
	}
}
