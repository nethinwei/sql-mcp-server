//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/nethinwei/sql-mcp-server/codegen"
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
	analyzed, err := prov.ExplainAnalyze(ctx, codegen.Compiled{
		SQL: "SELECT * FROM users", ReadOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if analyzed.ActualRows == 0 || analyzed.ExecutionTime <= 0 {
		t.Fatalf("EXPLAIN ANALYZE plan = %+v", analyzed)
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
			Enabled: config.Bool(true), SoftScore: 40, HardScore: 70, MaxRows: 10000,
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

func TestRLSRowFilterAndMasking(t *testing.T) {
	prov, cleanup := setupPG(t)
	defer cleanup()
	ctx := context.Background()
	cfg := &config.Config{
		Server:   config.ServerConfig{Role: "reader"},
		Database: config.DatabaseConfig{Driver: "postgres", DSN: "ignored"},
		Entities: []config.EntityConfig{{
			Name: "users", Source: "users", Kind: "table", PrimaryKey: []string{"id"},
			Fields: []config.FieldConfig{
				{Name: "id"}, {Name: "email", Mask: "email"}, {Name: "tenant_id"},
			},
			Roles: config.RoleConfig{Read: []string{"reader"}},
			RowPolicies: config.RowPolicies{
				"reader": config.FilterConfig{"op": "eq", "field": "tenant_id", "value": 7},
			},
		}},
		Tools: config.DefaultToolFlags(),
		Cost: config.CostConfig{
			// Score thresholds left disabled (0): this test verifies RLS +
			// masking, not the score gate. A small table is a Seq Scan (low
			// safety score) that EnforceCap bounds by MaxRows instead.
			Enabled: config.Bool(true), MaxRows: 10000, WhitelistPKPoint: true,
		},
	}
	cfg.ApplyDefaults()
	app, err := bootstrap.AssembleWithProvider(cfg, prov)
	if err != nil {
		t.Fatal(err)
	}
	tc := app.ToolContext("reader")

	// Read all: RLS restricts to tenant 7 (alice only); email is masked.
	in, _ := json.Marshal(map[string]any{"entity": "users"})
	res, err := tool.ReadTool{}.Run(ctx, in, tc)
	if err != nil {
		t.Fatalf("read should pass, got %v", err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("RLS should limit to 1 row (tenant 7), got %d", len(res.Content))
	}
	if res.Content[0]["email"] != "a***@x.com" {
		t.Fatalf("email not masked: %v", res.Content[0]["email"])
	}
}

func TestUpdateUnsafeWriteAndPK(t *testing.T) {
	prov, cleanup := setupPG(t)
	defer cleanup()
	ctx := context.Background()
	cfg := &config.Config{
		Server:   config.ServerConfig{Role: "writer"},
		Database: config.DatabaseConfig{Driver: "postgres", DSN: "ignored"},
		Entities: []config.EntityConfig{{
			Name: "users", Source: "users", Kind: "table", PrimaryKey: []string{"id"},
			Fields: []config.FieldConfig{{Name: "id"}, {Name: "email"}},
			Roles:  config.RoleConfig{Update: []string{"writer"}},
		}},
		Tools: config.DefaultToolFlags(),
		Cost:  config.CostConfig{Enabled: config.Bool(false)},
	}
	cfg.ApplyDefaults()
	app, err := bootstrap.AssembleWithProvider(cfg, prov)
	if err != nil {
		t.Fatal(err)
	}
	tc := app.ToolContext("writer")

	// No filter -> unsafe write rejected.
	unsafe, _ := json.Marshal(map[string]any{"entity": "users", "set": map[string]any{"email": "x@x.com"}})
	_, err = tool.UpdateTool{}.Run(ctx, unsafe, tc)
	if !errors.Is(err, tool.ErrUnsafeWrite) {
		t.Fatalf("expected ErrUnsafeWrite, got %v", err)
	}

	// By PK -> succeeds, one row affected.
	pkUpd, _ := json.Marshal(map[string]any{
		"entity": "users",
		"filter": []map[string]any{{"field": "id", "op": "eq", "value": 1}},
		"set":    map[string]any{"email": "new@x.com"},
	})
	res, err := tool.UpdateTool{}.Run(ctx, pkUpd, tc)
	if err != nil {
		t.Fatalf("PK update should succeed, got %v", err)
	}
	if res.Content[0]["rowsAffected"] != int64(1) {
		t.Fatalf("rowsAffected = %v, want 1", res.Content[0]["rowsAffected"])
	}
}

func TestEnforceCapLimitsRows(t *testing.T) {
	prov, cleanup := setupPG(t)
	defer cleanup()
	ctx := context.Background()
	cfg := &config.Config{
		Server:   config.ServerConfig{Role: "reader"},
		Database: config.DatabaseConfig{Driver: "postgres", DSN: "ignored"},
		Entities: []config.EntityConfig{{
			Name: "users", Source: "users", Kind: "table", PrimaryKey: []string{"id"},
			Fields: []config.FieldConfig{{Name: "id"}, {Name: "email"}},
			Roles:  config.RoleConfig{Read: []string{"reader"}},
		}},
		Tools: config.DefaultToolFlags(),
		// Score thresholds disabled (0) so Estimate passes on a Seq Scan;
		// EnforceCap deterministically wraps the query in LIMIT 1 (MaxRows).
		Cost: config.CostConfig{
			Enabled: config.Bool(true), MaxRows: 1,
		},
	}
	cfg.ApplyDefaults()
	app, err := bootstrap.AssembleWithProvider(cfg, prov)
	if err != nil {
		t.Fatal(err)
	}
	tc := app.ToolContext("reader")

	in, _ := json.Marshal(map[string]any{"entity": "users"})
	res, err := tool.ReadTool{}.Run(ctx, in, tc)
	if err != nil {
		t.Fatalf("should pass gate and execute, got %v", err)
	}
	if len(res.Content) > 1 {
		t.Fatalf("EnforceCap should limit to 1 row, got %d", len(res.Content))
	}
}

func TestPGExecuteProcedure(t *testing.T) {
	prov, cleanup := setupPG(t)
	defer cleanup()
	ctx := context.Background()
	_, _ = prov.ExecContext(ctx, "CREATE PROCEDURE noop_proc() LANGUAGE plpgsql AS $$ BEGIN END $$")
	cfg := &config.Config{
		Server:   config.ServerConfig{Role: "caller"},
		Database: config.DatabaseConfig{Driver: "postgres", DSN: "ignored"},
		Entities: []config.EntityConfig{{
			Name: "noop_proc", Source: "noop_proc", Kind: "procedure",
			Roles: config.RoleConfig{Execute: []string{"caller"}},
		}},
		Tools: config.DefaultToolFlags(),
		Cost:  config.CostConfig{Enabled: config.Bool(false)},
	}
	cfg.ApplyDefaults()
	app, err := bootstrap.AssembleWithProvider(cfg, prov)
	if err != nil {
		t.Fatal(err)
	}
	tc := app.ToolContext("caller")
	in, _ := json.Marshal(map[string]any{"entity": "noop_proc"})
	_, err = tool.ExecuteTool{}.Run(ctx, in, tc)
	if err != nil {
		t.Fatalf("execute should succeed, got %v", err)
	}
}
