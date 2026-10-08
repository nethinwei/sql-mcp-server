//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/tool"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
)

func TestPGCascadesAndTriggers(t *testing.T) {
	prov, cleanup := setupPG(t)
	defer cleanup()
	ctx := context.Background()
	for _, stmt := range []string{
		`CREATE TABLE parents (tenant integer, id integer, PRIMARY KEY (tenant, id))`,
		`CREATE TABLE kids (id serial PRIMARY KEY, tenant integer, parent_id integer,
		   FOREIGN KEY (tenant, parent_id) REFERENCES parents (tenant, id) ON DELETE CASCADE)`,
		`CREATE TABLE notes (id serial PRIMARY KEY, body text)`,
		`CREATE FUNCTION touch() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`,
		`CREATE TRIGGER notes_touch BEFORE UPDATE ON notes FOR EACH ROW EXECUTE FUNCTION touch()`,
		`INSERT INTO parents VALUES (1, 1)`,
		`INSERT INTO kids (tenant, parent_id) VALUES (1, 1)`,
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
	cascades := byName["parents"].Cascades
	if len(cascades) != 1 || cascades[0].Table != "kids" || cascades[0].OnDelete != entity.FKCascade ||
		!slices.Equal(cascades[0].Columns, []string{"tenant", "id"}) ||
		!slices.Equal(cascades[0].ForeignColumns, []string{"tenant", "parent_id"}) {
		t.Fatalf("parents cascades = %+v", cascades)
	}
	if !byName["notes"].SideEffects || byName["parents"].SideEffects {
		t.Fatalf("side effects: notes %v, parents %v", byName["notes"].SideEffects, byName["parents"].SideEffects)
	}
	if fks := byName["kids"].ForeignKeys; len(fks) != 1 || fks[0].OnDelete != entity.FKCascade {
		t.Fatalf("kids foreign keys = %+v", fks)
	}
	assertPGCascadeEnforced(t, ctx, prov)
}

func assertPGCascadeEnforced(t *testing.T, ctx context.Context, prov bootstrap.Provider) {
	t.Helper()
	cfg := &config.Config{
		Database: config.DatabaseConfig{Driver: "postgres", DSN: "ignored"},
		Cost:     config.CostConfig{Enabled: new(false)},
		Entities: []config.EntityConfig{{
			Name: "parents", Fields: []config.FieldConfig{{Name: "tenant"}, {Name: "id"}},
			Roles: config.RoleConfig{Delete: []string{"w"}},
		}},
	}
	cfg.ApplyDefaults()
	app, err := bootstrap.AssembleWithProvider(cfg, prov)
	if err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"entity":"parents","filter":[{"field":"tenant","op":"eq","value":1},` +
		`{"field":"id","op":"eq","value":1}]}`)
	if _, err := (tool.DeleteTool{}).Run(ctx, input, app.ToolContext("w")); !errors.Is(err, tool.ErrUnauthorized) {
		t.Fatalf("a delete cascading into the unexposed kids table: %v", err)
	}
}
