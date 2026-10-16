//go:build integration

package postgres_test

import (
	"context"
	"testing"
)

// Statistics are fresh once ANALYZE or autovacuum analyzed the table, or
// when the table is too small for them to matter; a table of the same name
// in another schema does not count for it.
func TestPGStatsFreshness(t *testing.T) {
	prov, cleanup := setupPG(t)
	defer cleanup()
	ctx := context.Background()
	// Autovacuum must not analyze the tables while the test looks.
	for _, q := range []string{
		`CREATE SCHEMA a`, `CREATE SCHEMA b`,
		`CREATE TABLE a.small (id int, v text) WITH (autovacuum_enabled = false)`,
		`INSERT INTO a.small SELECT g, 'x' FROM generate_series(1, 20) g`,
		`CREATE TABLE a.big (id int, v text) WITH (autovacuum_enabled = false)`,
		`INSERT INTO a.big SELECT g, repeat('x', 100) FROM generate_series(1, 20000) g`,
		`CREATE TABLE b.big (id int, v text) WITH (autovacuum_enabled = false)`,
		`INSERT INTO b.big SELECT g, 'x' FROM generate_series(1, 20000) g`,
	} {
		if _, err := prov.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	fresh := func(table string) bool {
		t.Helper()
		plan, err := prov.Explainer().Explain(ctx, "SELECT v FROM "+table+" WHERE v = 'y'", nil)
		if err != nil {
			t.Fatal(err)
		}
		return plan.StatsFresh
	}
	if !fresh("a.small") {
		t.Error("a small table never analyzed has statistics good enough")
	}
	if fresh("a.big") || fresh("b.big") {
		t.Error("a large table never analyzed has stale statistics")
	}
	if _, err := prov.ExecContext(ctx, `ANALYZE b.big`); err != nil {
		t.Fatal(err)
	}
	if !fresh("b.big") || fresh("a.big") {
		t.Error("analyzing b.big freshens it alone")
	}
}
