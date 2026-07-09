//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/nethinwei/sql-mcp-server/config"
	"github.com/nethinwei/sql-mcp-server/cost"
	"github.com/nethinwei/sql-mcp-server/entity"
	"github.com/nethinwei/sql-mcp-server/store"
	"github.com/nethinwei/sql-mcp-server/tool"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	pgprov "github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

func setupPG(t *testing.T) (*pgprov.Provider, func()) {
	t.Helper()
	ctx := context.Background()
	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	prov, err := pgprov.New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = prov.ExecContext(ctx,
		`CREATE TABLE users (id serial PRIMARY KEY, email text, tenant_id integer)`)
	_, _ = prov.ExecContext(ctx,
		`INSERT INTO users (email, tenant_id) VALUES ('alice@x.com', 7), ('bob@x.com', 8)`)
	return prov, func() {
		_ = prov.Close()
		_ = container.Terminate(context.Background())
	}
}

func TestProviderQueryExecExplainIntrospect(t *testing.T) {
	prov, cleanup := setupPG(t)
	defer cleanup()
	ctx := context.Background()

	rows, err := prov.QueryContext(ctx, "SELECT id, email FROM users ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	var got []store.Row
	for row, err := range store.Iter(rows) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, row)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}

	// EXPLAIN: an unfiltered select is a Seq Scan -> ScanFull.
	plan, err := prov.Explainer().Explain(ctx, "SELECT * FROM users", nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ScanType != cost.ScanFull {
		t.Fatalf("ScanType = %v, want ScanFull for unfiltered scan", plan.ScanType)
	}

	// EXPLAIN: a PK point lookup uses the index -> ScanPoint.
	plan2, err := prov.Explainer().Explain(ctx, "SELECT * FROM users WHERE id = $1", []any{1})
	if err != nil {
		t.Fatal(err)
	}
	if plan2.ScanType != cost.ScanIndex && plan2.ScanType != cost.ScanPoint {
		t.Fatalf("ScanType = %v, want index/point for PK lookup", plan2.ScanType)
	}

	// Introspect: discover the users table with a primary key.
	entities, err := prov.Introspector().Discover(ctx, []string{"public"})
	if err != nil {
		t.Fatal(err)
	}
	var users *entity.Entity
	for i := range entities {
		if entities[i].Name == "users" {
			users = &entities[i]
		}
	}
	if users == nil {
		t.Fatalf("users not discovered: %+v", entities)
	}
	pk := users.PrimaryKey()
	if len(pk) != 1 || pk[0] != "id" {
		t.Fatalf("primary key = %v, want [id]", pk)
	}
}

func TestCostGateEndToEnd(t *testing.T) {
	prov, cleanup := setupPG(t)
	defer cleanup()
	ctx := context.Background()

	cfg := &config.Config{
		Database: config.DatabaseConfig{Driver: "postgres", DSN: "ignored"},
		Entities: []config.EntityConfig{{
			Name: "users", Source: "users", Kind: "table", PrimaryKey: []string{"id"},
			Fields: []config.FieldConfig{{Name: "id"}, {Name: "email"}},
			Roles:  config.RoleConfig{Read: []string{"reader"}},
		}},
		Tools: config.DefaultToolFlags(),
		Cost: config.CostConfig{
			Enabled: true, SoftScore: 40, HardScore: 70, MaxRows: 10000,
			RejectFullScan: true, WhitelistPKPoint: true,
		},
	}
	cfg.ApplyDefaults()

	app, err := bootstrap.AssembleWithProvider(cfg, prov)
	if err != nil {
		t.Fatal(err)
	}
	tc := app.ToolContext("reader")

	// Full table scan (no filter) -> hard reject by the cost gate.
	fullScan, _ := json.Marshal(map[string]any{"entity": "users"})
	_, err = tool.ReadTool{}.Run(ctx, fullScan, tc)
	if !errors.Is(err, cost.ErrCostExceeded) {
		t.Fatalf("full scan should be rejected, got %v", err)
	}

	// PK point lookup -> whitelist bypass + execution succeeds.
	pkLookup, _ := json.Marshal(map[string]any{
		"entity": "users",
		"filter": []map[string]any{{"field": "id", "op": "eq", "value": 1}},
	})
	res, err := tool.ReadTool{}.Run(ctx, pkLookup, tc)
	if err != nil {
		t.Fatalf("PK point lookup should pass, got %v", err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("expected 1 row, got %v", res.Content)
	}
}
