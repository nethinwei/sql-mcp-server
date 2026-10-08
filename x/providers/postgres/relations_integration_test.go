//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/core/tool"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	pgprov "github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

func createPGRelations(t *testing.T, ctx context.Context, prov *pgprov.Provider) {
	t.Helper()
	for _, stmt := range []string{
		`CREATE VIEW active_users AS SELECT id, email FROM users WHERE tenant_id = 7`,
		`CREATE MATERIALIZED VIEW user_counts AS SELECT tenant_id, count(*) AS n FROM users GROUP BY tenant_id`,
		`CREATE TABLE events (id integer, at date) PARTITION BY RANGE (at)`,
		`CREATE TABLE events_2026 PARTITION OF events FOR VALUES FROM ('2026-01-01') TO ('2027-01-01')`,
	} {
		if _, err := prov.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}

// Views and materialized views are discovered (configured view entities used
// to fail startup as missing), partitions are not, and the server is named.
func TestPGDiscoversRelationKinds(t *testing.T) {
	prov, cleanup := setupPG(t)
	defer cleanup()
	ctx := context.Background()
	createPGRelations(t, ctx, prov)
	found, err := prov.Introspector().Discover(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]entity.Entity{}
	for _, e := range found {
		byName[e.Name] = e
	}
	if e := byName["users"]; e.Kind != entity.KindTable || e.Derived {
		t.Errorf("users = %+v", e)
	}
	if e := byName["events"]; e.Kind != entity.KindTable {
		t.Errorf("partitioned table events = %+v", e)
	}
	if _, ok := byName["events_2026"]; ok {
		t.Error("a partition must not be discovered: its rows are read through the partitioned table")
	}
	for _, name := range []string{"active_users", "user_counts"} {
		if e := byName[name]; e.Kind != entity.KindView || !e.Derived || len(e.Attributes) != 2 {
			t.Errorf("%s = %+v", name, e)
		}
	}
	physical, err := prov.Introspector().(introspect.PhysicalNamer).Physical(ctx)
	if err != nil || physical.Server == "" || physical.Catalog != "test" || physical.FoldCase {
		t.Fatalf("physical = %+v, %v", physical, err)
	}
}

func pgRelationsConfig(entities ...config.EntityConfig) *config.Config {
	cfg := &config.Config{
		Database: config.DatabaseConfig{Driver: "postgres", DSN: "ignored"},
		Cost:     config.CostConfig{Enabled: new(false)},
		Entities: entities,
	}
	cfg.ApplyDefaults()
	return cfg
}

func TestPGServesViewEntitiesAndRejectsDuplicates(t *testing.T) {
	prov, cleanup := setupPG(t)
	defer cleanup()
	ctx := context.Background()
	createPGRelations(t, ctx, prov)
	read := config.RoleConfig{Read: []string{"reader"}}
	app, err := bootstrap.AssembleWithProvider(pgRelationsConfig(
		config.EntityConfig{Name: "active_users", Kind: "view", Roles: read,
			Fields: []config.FieldConfig{{Name: "id"}, {Name: "email"}}},
		config.EntityConfig{Name: "user_counts", Roles: read,
			Fields: []config.FieldConfig{{Name: "tenant_id"}, {Name: "n"}}},
	), prov)
	if err != nil {
		t.Fatalf("view entities must assemble: %v", err)
	}
	for name, want := range map[string]int{"active_users": 1, "user_counts": 2} {
		in, _ := json.Marshal(map[string]any{"entity": name})
		res, err := tool.ReadTool{}.Run(ctx, in, app.ToolContext("reader"))
		if err != nil || len(res.Content) != want {
			t.Fatalf("read %s: %d rows, %v", name, len(res.Content), err)
		}
	}
	_, err = bootstrap.AssembleWithProvider(pgRelationsConfig(
		config.EntityConfig{Name: "users", Fields: []config.FieldConfig{{Name: "id"}}},
		config.EntityConfig{Name: "users_public", Schema: "public", Source: "users",
			Fields: []config.FieldConfig{{Name: "id"}}},
	), prov)
	if err == nil || !strings.Contains(err.Error(), "same relation") {
		t.Fatalf("two entities on public.users: %v", err)
	}
}
