//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/tool"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	pgprov "github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

// A write to a table others inherit from the old way (INHERITS) reaches
// their rows: their triggers and the foreign keys referencing them count on
// it too, and its keys, which do not cover their rows, serve no write.
func TestPGFoldsInheritingTablesIntoTheirParents(t *testing.T) {
	prov, cleanup := setupPG(t)
	defer cleanup()
	ctx := context.Background()
	for _, stmt := range []string{
		`CREATE TABLE items (id integer PRIMARY KEY, name text)`,
		`CREATE TABLE secret_items (PRIMARY KEY (id)) INHERITS (items)`,
		`CREATE TABLE audit (id integer PRIMARY KEY,
		   item_id integer REFERENCES secret_items ON DELETE CASCADE)`,
		`CREATE FUNCTION touch_item() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN OLD; END $$`,
		`CREATE TRIGGER secret_items_touch BEFORE DELETE ON secret_items FOR EACH ROW
		   EXECUTE PROCEDURE touch_item()`,
		`INSERT INTO secret_items VALUES (5, 'x')`,
		`INSERT INTO audit VALUES (1, 5)`,
	} {
		if _, err := prov.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	found, err := prov.Introspector().Discover(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]entity.Entity{}
	for _, e := range found {
		byName[e.Name] = e
	}
	for name, derived := range map[string]bool{"items": true, "secret_items": false} {
		e := byName[name]
		if e.Derived != derived || !e.SideEffects || len(e.Cascades) != 1 || e.Cascades[0].Table != "audit" {
			t.Errorf("%s: derived %v, side effects %v, cascades %+v; want derived %v, the trigger and audit",
				name, e.Derived, e.SideEffects, e.Cascades, derived)
		}
	}
	assertPGInheritedCascades(t, ctx, prov)
}

// assertPGInheritedCascades: deleting from the parent a row of a table
// inheriting it, which another table references, needs that table too.
func assertPGInheritedCascades(t *testing.T, ctx context.Context, prov *pgprov.Provider) {
	t.Helper()
	cfg := &config.Config{
		Database: config.DatabaseConfig{Driver: "postgres", DSN: "ignored"},
		Cost:     config.CostConfig{Enabled: new(false)},
		Entities: []config.EntityConfig{{Name: "items", Fields: []config.FieldConfig{{Name: "id"}, {Name: "name"}},
			Roles: config.RoleConfig{Delete: []string{"w"}}}},
	}
	cfg.ApplyDefaults()
	app, err := bootstrap.AssembleWithProvider(cfg, prov)
	if err != nil {
		t.Fatal(err)
	}
	in := json.RawMessage(`{"entity":"items","filter":[{"field":"id","op":"eq","value":5}]}`)
	if _, err := (tool.DeleteTool{}).Run(ctx, in, app.ToolContext("w")); !errors.Is(err, tool.ErrUnauthorized) {
		t.Errorf("a delete cascading from secret_items into the unexposed audit table: %v", err)
	}
	var left int
	if err := prov.DB().QueryRowContext(ctx, `SELECT count(*) FROM audit`).Scan(&left); err != nil || left != 1 {
		t.Errorf("audit rows = %d (%v), want the one the refused delete would have cascaded to", left, err)
	}
}
