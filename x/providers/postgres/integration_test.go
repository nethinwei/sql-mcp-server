//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/nethinwei/sql-mcp-server/core/codegen"
	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/core/tool"
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

	assertPGQueryResults(t, ctx, prov)
	assertPGExplainPlans(t, ctx, prov)
	assertPGIntrospectUsers(t, ctx, prov)
}

func assertPGQueryResults(t *testing.T, ctx context.Context, prov *pgprov.Provider) {
	t.Helper()
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
}

func assertPGExplainPlans(t *testing.T, ctx context.Context, prov *pgprov.Provider) {
	t.Helper()
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
	plan2, err := prov.Explainer().Explain(ctx, "SELECT * FROM users WHERE id = $1", []any{1})
	if err != nil {
		t.Fatal(err)
	}
	if plan2.ScanType != cost.ScanIndex && plan2.ScanType != cost.ScanPoint {
		t.Fatalf("ScanType = %v, want index/point for PK lookup", plan2.ScanType)
	}
}

func assertPGIntrospectUsers(t *testing.T, ctx context.Context, prov *pgprov.Provider) {
	t.Helper()
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
	app := newPGRLSApp(t, prov)
	assertPGMaskedRLS(t, ctx, app)
	assertPGAdversarialRLS(t, ctx, app)
	assertPGQuotedIdentifierRLS(t, ctx, prov)
}

func newPGRLSApp(t *testing.T, prov *pgprov.Provider) *bootstrap.App {
	t.Helper()
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
		// This test targets RLS/masking; mandatory EnforceCap remains active
		// while optional EXPLAIN scoring is disabled.
		Cost: config.CostConfig{Enabled: config.Bool(false), MaxRows: 10000},
	}
	cfg.ApplyDefaults()
	app, err := bootstrap.AssembleWithProvider(cfg, prov)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func assertPGMaskedRLS(t *testing.T, ctx context.Context, app *bootstrap.App) {
	t.Helper()
	in, _ := json.Marshal(map[string]any{"entity": "users"})
	res, err := tool.ReadTool{}.Run(ctx, in, app.ToolContext("reader"))
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

func assertPGAdversarialRLS(t *testing.T, ctx context.Context, app *bootstrap.App) {
	t.Helper()
	for _, tc := range []struct {
		name   string
		filter []map[string]any
	}{
		{name: "request other tenant", filter: []map[string]any{{"field": "tenant_id", "op": "eq", "value": 8}}},
		{name: "negate allowed tenant", filter: []map[string]any{{"field": "tenant_id", "op": "ne", "value": 7}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, _ := json.Marshal(map[string]any{"entity": "users", "filter": tc.filter})
			got, err := tool.ReadTool{}.Run(ctx, in, app.ToolContext("reader"))
			if err != nil {
				t.Fatalf("adversarial read failed: %v", err)
			}
			if len(got.Content) != 0 {
				t.Fatalf("row policy was weakened: %v", got.Content)
			}
		})
	}
}

func assertPGQuotedIdentifierRLS(t *testing.T, ctx context.Context, prov *pgprov.Provider) {
	t.Helper()
	if _, err := prov.ExecContext(ctx, `CREATE SCHEMA "tenant""edge"`); err != nil {
		t.Fatal(err)
	}
	if _, err := prov.ExecContext(ctx,
		`CREATE TABLE "tenant""edge"."user""records" (id integer PRIMARY KEY, tenant_id integer)`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := prov.ExecContext(ctx,
		`INSERT INTO "tenant""edge"."user""records" VALUES (1, 7), (2, 8)`,
	); err != nil {
		t.Fatal(err)
	}
	quotedCfg := &config.Config{
		Server:   config.ServerConfig{Role: "reader"},
		Database: config.DatabaseConfig{Driver: "postgres", DSN: "ignored"},
		Entities: []config.EntityConfig{{
			Name: "quoted_users", Source: `user"records`, Schema: `tenant"edge`, Kind: "table",
			PrimaryKey: []string{"id"},
			Fields:     []config.FieldConfig{{Name: "id"}, {Name: "tenant_id"}},
			Roles:      config.RoleConfig{Read: []string{"reader"}},
			RowPolicies: config.RowPolicies{
				"reader": config.FilterConfig{"op": "eq", "field": "tenant_id", "value": 7},
			},
		}},
		Tools: config.DefaultToolFlags(),
		Cost:  config.CostConfig{Enabled: config.Bool(false), MaxRows: 10000},
	}
	quotedCfg.ApplyDefaults()
	quotedApp, err := bootstrap.AssembleWithProvider(quotedCfg, prov)
	if err != nil {
		t.Fatal(err)
	}
	quotedInput, _ := json.Marshal(map[string]any{
		"entity": "quoted_users",
		"filter": []map[string]any{{"field": "tenant_id", "op": "eq", "value": 8}},
	})
	quotedResult, err := tool.ReadTool{}.Run(ctx, quotedInput, quotedApp.ToolContext("reader"))
	if err != nil {
		t.Fatalf("quoted schema/table read failed: %v", err)
	}
	if len(quotedResult.Content) != 0 {
		t.Fatalf("quoted identifier read weakened row policy: %v", quotedResult.Content)
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
		// Optional Estimate is disabled; mandatory EnforceCap remains active.
		Cost: config.CostConfig{
			Enabled: config.Bool(false), MaxRows: 1,
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
			MCP:   config.MCPFlags{TrustedProcedure: true},
		}},
		Tools: config.DefaultToolFlags(),
		Cost: config.CostConfig{
			Enabled:        config.Bool(false),
			AllowTemplates: []string{`CALL "noop_proc"()`},
		},
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
