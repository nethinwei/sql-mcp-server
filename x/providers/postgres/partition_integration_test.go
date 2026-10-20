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
)

// A partition's foreign keys, cascades and triggers belong to its
// partitioned table, the entity: a write to the table reaches them.
func TestPGFoldsPartitionsIntoTheirTable(t *testing.T) {
	prov, cleanup := setupPG(t)
	defer cleanup()
	ctx := context.Background()
	createPGPartitions(t, ctx, prov)
	found, err := prov.Introspector().Discover(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]entity.Entity{}
	for _, e := range found {
		byName[e.Name] = e
	}
	if c := byName["refd"].Cascades; len(c) != 1 || c[0].Table != "refs" {
		t.Errorf("refd cascades = %+v, want refs (it references a partition of refd)", c)
	}
	if c := byName["customers"].Cascades; len(c) != 1 || c[0].Table != "orders" {
		t.Errorf("customers cascades = %+v, want orders alone (not its partitions)", c)
	}
	if fks := byName["orders"].ForeignKeys; len(fks) != 1 {
		t.Errorf("orders foreign keys = %+v, want one", fks)
	}
	if !byName["tp"].SideEffects {
		t.Error("tp has no side effects, though a trigger on its partition runs on its writes")
	}
	assertPGPartitionCascades(t, ctx, prov)
}

// createPGPartitions creates a foreign key referencing a partition, one of a
// partitioned table (copied onto its partitions), and a trigger on a
// partition.
func createPGPartitions(t *testing.T, ctx context.Context, prov bootstrap.Provider) {
	t.Helper()
	for _, stmt := range []string{
		// A foreign key that references a partition.
		`CREATE TABLE refd (id integer, at date, PRIMARY KEY (id, at)) PARTITION BY RANGE (at)`,
		`CREATE TABLE refd_2026 PARTITION OF refd FOR VALUES FROM ('2026-01-01') TO ('2027-01-01')`,
		`CREATE TABLE refs (id integer PRIMARY KEY, refid integer, at date,
		   FOREIGN KEY (refid, at) REFERENCES refd_2026 (id, at) ON DELETE CASCADE)`,
		`INSERT INTO refd VALUES (1, '2026-06-01')`,
		`INSERT INTO refs VALUES (1, 1, '2026-06-01')`,
		// A foreign key of a partitioned table, cloned onto its partitions.
		`CREATE TABLE customers (id integer PRIMARY KEY)`,
		`CREATE TABLE orders (id integer, at date, customer_id integer REFERENCES customers ON DELETE CASCADE,
		   PRIMARY KEY (id, at)) PARTITION BY RANGE (at)`,
		`CREATE TABLE orders_2026 PARTITION OF orders FOR VALUES FROM ('2026-01-01') TO ('2027-01-01')`,
		`INSERT INTO customers VALUES (1)`,
		`INSERT INTO orders VALUES (1, '2026-06-01', 1)`,
		// A trigger on a partition.
		`CREATE TABLE tp (id integer, at date) PARTITION BY RANGE (at)`,
		`CREATE TABLE tp1 PARTITION OF tp FOR VALUES FROM ('2026-01-01') TO ('2027-01-01')`,
		`CREATE FUNCTION touch() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`,
		`CREATE TRIGGER tp1_touch BEFORE INSERT ON tp1 FOR EACH ROW EXECUTE PROCEDURE touch()`,
	} {
		if _, err := prov.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}

// assertPGPartitionCascades: deleting from a table whose partition another
// table references needs that table too; deleting what a partitioned table
// references needs the partitioned table alone.
func assertPGPartitionCascades(t *testing.T, ctx context.Context, prov bootstrap.Provider) {
	t.Helper()
	cfg := &config.Config{
		Database: config.DatabaseConfig{Driver: "postgres", DSN: "ignored"},
		Cost:     config.CostConfig{Enabled: new(false)},
		Entities: []config.EntityConfig{
			{Name: "refd", Fields: []config.FieldConfig{{Name: "id"}, {Name: "at"}},
				Roles: config.RoleConfig{Delete: []string{"w"}}},
			{Name: "customers", Fields: []config.FieldConfig{{Name: "id"}},
				Roles: config.RoleConfig{Delete: []string{"w"}}},
			{Name: "orders", Fields: []config.FieldConfig{{Name: "id"}, {Name: "at"}, {Name: "customer_id"}},
				Roles: config.RoleConfig{Delete: []string{"w"}}},
		},
	}
	cfg.ApplyDefaults()
	app, err := bootstrap.AssembleWithProvider(cfg, prov)
	if err != nil {
		t.Fatal(err)
	}
	tc := app.ToolContext("w")
	refd := json.RawMessage(`{"entity":"refd","filter":[{"field":"id","op":"eq","value":1},` +
		`{"field":"at","op":"eq","value":"2026-06-01"}]}`)
	if _, err := (tool.DeleteTool{}).Run(ctx, refd, tc); !errors.Is(err, tool.ErrUnauthorized) {
		t.Errorf("a delete cascading into the unexposed refs table: %v", err)
	}
	customer := json.RawMessage(`{"entity":"customers","filter":[{"field":"id","op":"eq","value":1}]}`)
	if _, err := (tool.DeleteTool{}).Run(ctx, customer, tc); err != nil {
		t.Errorf("a delete cascading into orders, which the caller may delete from: %v", err)
	}
}
