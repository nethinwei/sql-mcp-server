//go:build integration

// Hologres integration tests run against a real instance supplied via
// HOLOGRES_TEST_DSN (pgx format, e.g.
// postgres://$AK_ID:$AK_SECRET@$ENDPOINT:80/$DB). There is no official
// Hologres container image, so the suite skips when the variable is unset;
// these tests are the evidence base recorded in docs/supported-versions.md.

package hologres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/core/tool"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	hgprov "github.com/nethinwei/sql-mcp-server/x/providers/hologres"
)

// itSchema isolates this suite's tables on the shared instance.
const itSchema = "sqlmcp_it"

func hologresDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("HOLOGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("HOLOGRES_TEST_DSN not set; skipping Hologres integration tests")
	}
	return dsn
}

func execAll(t *testing.T, ctx context.Context, prov *hgprov.Provider, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if _, err := prov.ExecContext(ctx, s); err != nil {
			t.Fatalf("exec %q: %v", s, err)
		}
	}
}

func setupHG(t *testing.T) (*hgprov.Provider, func()) {
	t.Helper()
	prov, err := hgprov.New(hologresDSN(t))
	if err != nil {
		t.Fatalf("open hologres: %v", err)
	}
	ctx := context.Background()
	execAll(t, ctx, prov,
		"DROP TABLE IF EXISTS "+itSchema+".hg_it_events_cn",
		"DROP TABLE IF EXISTS "+itSchema+".hg_it_events",
		"DROP TABLE IF EXISTS "+itSchema+".users",
		"DROP SCHEMA IF EXISTS "+itSchema,
		"CREATE SCHEMA "+itSchema,
		"CREATE TABLE "+itSchema+".users (id integer PRIMARY KEY, email text, tenant_id integer)",
		"INSERT INTO "+itSchema+".users VALUES (1, 'alice@x.com', 7), (2, 'bob@x.com', 8)",
	)
	return prov, func() {
		execAll(t, ctx, prov,
			"DROP TABLE IF EXISTS "+itSchema+".hg_it_events_cn",
			"DROP TABLE IF EXISTS "+itSchema+".hg_it_events",
			"DROP TABLE IF EXISTS "+itSchema+".users",
			"DROP SCHEMA IF EXISTS "+itSchema,
		)
		_ = prov.Close()
	}
}

// TestHologresConnectionParams proves the injected session parameters reach
// the server: the DB-native statement timeout and the application_name tag
// used by Hologres slow-query diagnostics.
func TestHologresConnectionParams(t *testing.T) {
	dsn := hologresDSN(t)
	prov, err := hgprov.NewWithTimeout(dsn, 5*time.Second)
	if err != nil {
		t.Fatalf("open hologres: %v", err)
	}
	defer func() { _ = prov.Close() }()
	ctx := context.Background()
	for _, probe := range []struct{ guc, want string }{
		{"statement_timeout", "5000"},
		{"application_name", "sql-mcp-server"},
	} {
		rows, err := prov.QueryContext(ctx, "SHOW "+probe.guc)
		if err != nil {
			t.Fatalf("show %s: %v", probe.guc, err)
		}
		var value string
		for row, err := range store.Iter(rows) {
			if err != nil {
				t.Fatalf("read %s: %v", probe.guc, err)
			}
			if len(row) != 1 {
				t.Fatalf("%s returned %d columns", probe.guc, len(row))
			}
			for _, v := range row {
				value = fmt.Sprint(v)
			}
			break
		}
		_ = rows.Close()
		if value != probe.want {
			t.Fatalf("%s = %q, want %q", probe.guc, value, probe.want)
		}
	}
}

// TestHologresQueryIntrospectExplain covers the core provider surface: point
// queries through the simple protocol, catalog introspection with partition
// children hidden, and the conservative text EXPLAIN parser.
func TestHologresQueryIntrospectExplain(t *testing.T) {
	prov, cleanup := setupHG(t)
	defer cleanup()
	ctx := context.Background()

	execAll(t, ctx, prov,
		"CREATE TABLE "+itSchema+".hg_it_events (region text NOT NULL, id integer NOT NULL, val integer, PRIMARY KEY (region, id)) PARTITION BY LIST (region)",
		"CREATE TABLE "+itSchema+".hg_it_events_cn PARTITION OF "+itSchema+".hg_it_events FOR VALUES IN ('cn')",
		"INSERT INTO "+itSchema+".hg_it_events VALUES ('cn', 1, 10), ('cn', 2, 20)",
	)

	// Parameterized point query through the simple protocol.
	rows, err := prov.QueryContext(ctx,
		"SELECT id, email FROM "+itSchema+".users WHERE id = $1", 1)
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
	if len(got) != 1 || got[0]["email"] != "alice@x.com" {
		t.Fatalf("point query rows = %v", got)
	}

	// Introspection: users with its PK, the partition parent listed, the
	// partition child hidden.
	entities, err := prov.Introspector().Discover(ctx, []string{itSchema})
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]entity.Entity{}
	for _, e := range entities {
		found[e.Name] = e
	}
	users, ok := found["users"]
	if !ok {
		t.Fatalf("users not discovered: %v", entityNames(entities))
	}
	if pk := users.PrimaryKey(); len(pk) != 1 || pk[0] != "id" {
		t.Fatalf("users primary key = %v, want [id]", pk)
	}
	if _, ok := found["hg_it_events"]; !ok {
		t.Fatalf("partition parent not discovered: %v", entityNames(entities))
	}
	if _, ok := found["hg_it_events_cn"]; ok {
		t.Fatal("partition child leaked into discovery")
	}

	// Text EXPLAIN parses without error and classifies the unfiltered scan:
	// the capability profile keeps this out of enforcement, so the assertion
	// only proves the parser understands Hologres' real output format.
	plan, err := prov.Explainer().Explain(ctx, "SELECT * FROM "+itSchema+".users", nil)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if plan.ScanType == cost.ScanUnknown {
		t.Fatalf("explain text not recognized, plan = %+v", plan)
	}
}

func entityNames(entities []entity.Entity) []string {
	names := make([]string, 0, len(entities))
	for _, e := range entities {
		names = append(names, e.Name)
	}
	return names
}

// TestHologresReadEnforceCap proves the mandatory row cap rewrites reads on
// Hologres even though no EXPLAIN Estimate layer is assembled for it.
func TestHologresReadEnforceCap(t *testing.T) {
	prov, cleanup := setupHG(t)
	defer cleanup()
	ctx := context.Background()
	app := assembleHG(t, prov, func(c *config.Config) {
		c.Cost = config.CostConfig{Enabled: config.Bool(false), MaxRows: 1}
	})
	in, _ := json.Marshal(map[string]any{"entity": "users"})
	res, err := tool.ReadTool{}.Run(ctx, in, app.ToolContext("reader"))
	if err != nil {
		t.Fatalf("read should pass gate and execute, got %v", err)
	}
	if len(res.Content) > 1 {
		t.Fatalf("EnforceCap should limit to 1 row, got %d", len(res.Content))
	}
}

// TestHologresRLSRowFilterAndMasking proves row policies and field masking
// hold on Hologres, including adversarial filters and quoted identifiers.
func TestHologresRLSRowFilterAndMasking(t *testing.T) {
	prov, cleanup := setupHG(t)
	defer cleanup()
	ctx := context.Background()
	app := assembleHG(t, prov, nil)

	in, _ := json.Marshal(map[string]any{"entity": "users"})
	res, err := tool.ReadTool{}.Run(ctx, in, app.ToolContext("reader"))
	if err != nil {
		t.Fatalf("read should pass, got %v", err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("row policy should limit to 1 row, got %d", len(res.Content))
	}
	if res.Content[0]["email"] != "a***@x.com" {
		t.Fatalf("email not masked: %v", res.Content[0]["email"])
	}

	for _, f := range []map[string]any{
		{"field": "tenant_id", "op": "eq", "value": 8},
		{"field": "tenant_id", "op": "ne", "value": 7},
	} {
		adversarial, _ := json.Marshal(map[string]any{"entity": "users", "filter": []map[string]any{f}})
		got, err := tool.ReadTool{}.Run(ctx, adversarial, app.ToolContext("reader"))
		if err != nil {
			t.Fatalf("adversarial read failed: %v", err)
		}
		if len(got.Content) != 0 {
			t.Fatalf("row policy weakened by filter %v: %v", f, got.Content)
		}
	}

	execAll(t, ctx, prov,
		`CREATE SCHEMA "hg""edge"`,
		`CREATE TABLE "hg""edge"."user""records" (id integer PRIMARY KEY, tenant_id integer)`,
		`INSERT INTO "hg""edge"."user""records" VALUES (1, 7), (2, 8)`,
	)
	quoted := assembleHG(t, prov, func(c *config.Config) {
		c.Entities[0].Name = "quoted_users"
		c.Entities[0].Source = `user"records`
		c.Entities[0].Schema = `hg"edge`
	})
	quotedInput, _ := json.Marshal(map[string]any{
		"entity": "quoted_users",
		"filter": []map[string]any{{"field": "tenant_id", "op": "eq", "value": 8}},
	})
	quotedResult, err := tool.ReadTool{}.Run(ctx, quotedInput, quoted.ToolContext("reader"))
	if err != nil {
		t.Fatalf("quoted identifier read failed: %v", err)
	}
	if len(quotedResult.Content) != 0 {
		t.Fatalf("quoted identifier read weakened row policy: %v", quotedResult.Content)
	}
}

// TestHologresTransactionUnsupported proves begin_transaction fails closed
// end to end: Hologres transactions cover DDL only, so tokens would promise
// atomicity the engine cannot deliver.
func TestHologresTransactionUnsupported(t *testing.T) {
	prov, cleanup := setupHG(t)
	defer cleanup()
	app := assembleHG(t, prov, nil)
	in, _ := json.Marshal(map[string]any{})
	_, err := tool.BeginTransactionTool{}.Run(context.Background(), in, app.ToolContext("reader"))
	if !errors.Is(err, tool.ErrTransactionUnsupported) {
		t.Fatalf("begin_transaction error = %v, want ErrTransactionUnsupported", err)
	}
	denial, ok := tool.DenialFor(err, "decision-it")
	if !ok || denial.Code != tool.CodeTransactionUnsupported {
		t.Fatalf("denial = %+v, ok = %v", denial, ok)
	}
}

func assembleHG(t *testing.T, prov *hgprov.Provider, mutate func(*config.Config)) *bootstrap.App {
	t.Helper()
	cfg := &config.Config{
		Server:   config.ServerConfig{Role: "reader"},
		Database: config.DatabaseConfig{Driver: "hologres", DSN: "ignored"},
		Entities: []config.EntityConfig{{
			Name: "users", Source: "users", Schema: itSchema, Kind: "table", PrimaryKey: []string{"id"},
			Fields: []config.FieldConfig{
				{Name: "id"}, {Name: "email", Mask: "email"}, {Name: "tenant_id"},
			},
			Roles: config.RoleConfig{Read: []string{"reader"}},
			RowPolicies: config.RowPolicies{
				"reader": config.FilterConfig{"op": "eq", "field": "tenant_id", "value": 7},
			},
		}},
		Tools: config.DefaultToolFlags(),
		Cost:  config.CostConfig{Enabled: config.Bool(false), MaxRows: 10000},
	}
	if mutate != nil {
		mutate(cfg)
	}
	cfg.ApplyDefaults()
	app, err := bootstrap.AssembleWithProvider(cfg, prov)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	return app
}
