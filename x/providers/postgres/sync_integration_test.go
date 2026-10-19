//go:build integration

package postgres_test

import (
	"context"
	"slices"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/configedit"
	"github.com/nethinwei/sql-mcp-server/x/configplan"
)

// The console's entity sync against a real database: after a column is
// dropped, a table dropped and a primary key changed, one sync of the
// entities, applied, leaves no entity unavailable.
func TestPGSyncEntitiesAfterSchemaChanges(t *testing.T) {
	prov, cleanup := setupPG(t)
	defer cleanup()
	ctx := context.Background()
	exec := func(stmts ...string) {
		for _, stmt := range stmts {
			if _, err := prov.ExecContext(ctx, stmt); err != nil {
				t.Fatalf("%s: %v", stmt, err)
			}
		}
	}
	exec(`CREATE TABLE customers (id int PRIMARY KEY, name text, legacy text)`,
		`CREATE TABLE orders (id int PRIMARY KEY, customer_id int NOT NULL, note text)`,
		`CREATE TABLE dropped (id int PRIMARY KEY)`)
	cfg := syncConfig()
	exec(`ALTER TABLE customers DROP COLUMN legacy`, `DROP TABLE dropped`,
		`ALTER TABLE orders DROP CONSTRAINT orders_pkey`, `ALTER TABLE orders ADD PRIMARY KEY (id, customer_id)`)
	app, err := bootstrap.AssembleWithProvider(cfg, prov)
	if err != nil {
		t.Fatal(err)
	}
	missing := app.Missing()
	if len(app.Unavailable) != 2 || !missing.Tables["default.public.dropped"] ||
		!slices.Equal(missing.Fields["default.public.customers"], []string{"legacy"}) {
		t.Fatalf("unavailable = %v, missing = %+v", app.Unavailable, missing)
	}
	plan := configplan.Plan{
		Scope: configplan.Scope{Entities: []string{
			"default.public.customers", "default.public.orders", "default.public.dropped",
		}},
		Sync: &configplan.SyncRule{Remove: true, Keys: true, RemoveTables: true},
	}
	// Without a scan, what the server found missing is enough for the
	// columns and the table; the keys need a scan.
	unscanned := []configplan.Source{{Datasource: "default", Catalog: introspect.Catalog{Default: "public"}}}
	synced := syncAndAssemble(t, prov, cfg, missing, unscanned, plan)
	if len(synced.Unavailable) != 0 {
		t.Fatalf("unavailable after a sync without a scan: %v", synced.Unavailable)
	}
	cat, err := introspect.LoadCatalog(ctx, prov.Introspector(), []string{"public"}, false)
	if err != nil {
		t.Fatal(err)
	}
	scannedSources := []configplan.Source{{Datasource: "default", Catalog: cat, Scanned: []string{"public"}}}
	synced = syncAndAssemble(t, prov, cfg, missing, scannedSources, plan)
	orders, ok := synced.Registry.Resolve("default.public.orders")
	if len(synced.Unavailable) != 0 || !ok || !slices.Equal(orders.Entity.PrimaryKey(), []string{"id", "customer_id"}) {
		t.Fatalf("unavailable = %v, orders keys = %+v", synced.Unavailable, orders.Entity.Keys)
	}
}

func syncConfig() *config.Config {
	cfg := &config.Config{
		Database: config.DatabaseConfig{Driver: "postgres", DSN: "ignored"},
		Cost:     config.CostConfig{Enabled: new(false)},
		Entities: []config.EntityConfig{
			{Name: "customers", Schema: "public", PrimaryKey: []string{"id"},
				Fields: []config.FieldConfig{{Name: "id"}, {Name: "name"}, {Name: "legacy"}}},
			{Name: "orders", Schema: "public", PrimaryKey: []string{"id"},
				Fields: []config.FieldConfig{{Name: "id"}, {Name: "customer_id"}, {Name: "note"}}},
			{Name: "dropped", Schema: "public", PrimaryKey: []string{"id"}, Fields: []config.FieldConfig{{Name: "id"}}},
		},
	}
	cfg.ApplyDefaults()
	return cfg
}

// syncAndAssemble evaluates plan, applies it through the configuration chain
// as the admin API does, and assembles the outcome.
func syncAndAssemble(
	t *testing.T, prov bootstrap.Provider, cfg *config.Config, missing bootstrap.Missing,
	sources []configplan.Source, plan configplan.Plan,
) *bootstrap.App {
	t.Helper()
	p, err := configplan.Evaluate(configplan.Input{Base: cfg, Published: cfg, Sources: sources,
		Missing: missing.Tables, MissingFields: missing.Fields, MissingRowColumns: missing.RowColumns}, plan)
	if err != nil {
		t.Fatal(err)
	}
	_, next, _, err := configedit.ApplySpec(config.SpecOf(cfg), p.Edit())
	if err != nil {
		t.Fatalf("the sync does not validate: %v", err)
	}
	app, err := bootstrap.AssembleWithProvider(next, prov)
	if err != nil {
		t.Fatal(err)
	}
	return app
}
