// Package escapetest runs the SQL escape hatch (execute_sql) against a real
// database, the same way for every provider: a query, a write, a DDL the
// required estimate refuses, the same DDL with the estimate skipped, and the
// audit file holding the statements.
package escapetest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/tool"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
)

// Run exercises execute_sql on prov (driver's), whose users table (rows 1
// and 2 with id, email and tenant_id) is named table; probe names the table
// the DDL creates. Both may be schema-qualified.
func Run(t *testing.T, driver string, prov bootstrap.Provider, table, probe string) {
	t.Helper()
	ctx := context.Background()
	auditPath := t.TempDir() + "/audit.jsonl"
	tc := assemble(t, driver, prov, auditPath, config.SQLEscapeEstimateRequired)
	estimated(t, ctx, tc, table)
	metadata(t, ctx, tc, driver, table)
	ddl := map[string]any{"sql": "CREATE TABLE " + probe + " (id int)", "mode": "exec"}
	_, err := call(ctx, tc, ddl)
	var unestimable *tool.UnestimableError
	if !errors.As(err, &unestimable) || !errors.Is(err, cost.ErrCostExceeded) {
		t.Fatalf("a DDL the estimate cannot judge must be refused while required: %v", err)
	}
	skipping := assemble(t, driver, prov, auditPath, config.SQLEscapeEstimateSkip)
	if res := run(t, ctx, skipping, ddl); res["rowsAffected"] == nil {
		t.Fatalf("DDL with the estimate skipped = %v", res)
	}
	audit, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"phase":"begin"`, `"phase":"end"`, `"rowsAffected":2`, `"sql":"UPDATE ` + table} {
		if !strings.Contains(string(audit), want) {
			t.Fatalf("audit file lacks %s:\n%s", want, audit)
		}
	}
}

// estimated runs what the required estimate must pass: a keyed query and
// write, and statements reading no table (a constant SELECT, an INSERT ...
// VALUES), whose plans have no scan to judge.
func estimated(t *testing.T, ctx context.Context, tc tool.Context, table string) {
	t.Helper()
	res := run(t, ctx, tc, map[string]any{"sql": "SELECT id, email FROM " + table + " ORDER BY id"})
	if res["rowCount"] != 2 || res["truncated"] != false {
		t.Fatalf("query = %v", res)
	}
	if cols, _ := res["columns"].([]string); len(cols) != 2 || cols[0] != "id" {
		t.Fatalf("columns = %v", res["columns"])
	}
	// Keyed, as the governed plane's writes are: an unkeyed UPDATE is a full
	// scan the required estimate refuses, and MySQL's safe-update mode (the
	// provider's default) refuses it too.
	update := map[string]any{
		"sql": "UPDATE " + table + " SET tenant_id = tenant_id + 1 WHERE id IN (1, 2)", "mode": "exec",
	}
	if res = run(t, ctx, tc, update); res["rowsAffected"] != int64(2) {
		t.Fatalf("exec = %v", res)
	}
	if res = run(t, ctx, tc, map[string]any{"sql": "SELECT 1 AS one"}); res["rowCount"] != 1 {
		t.Fatalf("constant query = %v", res)
	}
	insert := map[string]any{
		"sql": "INSERT INTO " + table + " (id, email, tenant_id) VALUES (1000, 'carol@x.com', 9)", "mode": "exec",
	}
	if res = run(t, ctx, tc, insert); res["rowsAffected"] != int64(1) {
		t.Fatalf("insert = %v", res)
	}
	cleanup := map[string]any{"sql": "DELETE FROM " + table + " WHERE id = 1000", "mode": "exec"}
	if res = run(t, ctx, tc, cleanup); res["rowsAffected"] != int64(1) {
		t.Fatalf("delete = %v", res)
	}
}

// metadata runs SHOW and DESCRIBE statements, which EXPLAIN cannot
// estimate: they run under the hard limits alone (cost.sqlEscape.metadata
// allows them by default), while DESCRIBE used as EXPLAIN ANALYZE, which
// would run the statement, is estimated and refused.
func metadata(t *testing.T, ctx context.Context, tc tool.Context, driver, table string) {
	t.Helper()
	statements := []string{"SHOW search_path"}
	if driver != "postgres" {
		show := "SHOW TABLES" // of the connection's database, or of the table's when it names one
		if i := strings.LastIndex(table, "."); i > 0 {
			show += " FROM " + table[:i]
		}
		statements = []string{show, "SHOW INDEX FROM " + table, "DESCRIBE " + table}
	}
	for _, sql := range statements {
		if res := run(t, ctx, tc, map[string]any{"sql": sql}); res["rowCount"] == 0 {
			t.Fatalf("%s returned no rows: %v", sql, res)
		}
	}
	if driver != "postgres" {
		_, err := call(ctx, tc, map[string]any{"sql": "DESCRIBE ANALYZE SELECT * FROM " + table})
		if !errors.Is(err, cost.ErrCostExceeded) {
			t.Fatalf("DESCRIBE ANALYZE must be estimated (and refused): %v", err)
		}
	}
}

func assemble(t *testing.T, driver string, prov bootstrap.Provider, auditPath, estimate string) tool.Context {
	t.Helper()
	cfg := &config.Config{
		Server: config.ServerConfig{User: "alice"},
		Database: config.DatabaseConfig{
			Driver: driver, DSN: "ignored", SQLEscape: config.SQLEscapeConfig{Connection: config.DefaultConnection},
		},
		Roles: map[string]config.RoleDefinition{"dba": {Permissions: []string{"sql:execute@default"}}},
		Users: map[string]config.UserConfig{"alice": {Roles: []string{"dba"}}},
		Tools: config.DefaultToolFlags(),
		Cost:  config.CostConfig{SQLEscape: config.SQLEscapeCostConfig{Estimate: estimate}},
		Audit: config.AuditConfig{Enabled: true, Path: auditPath},
	}
	cfg.Tools.ExecuteSQL = true
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	app, err := bootstrap.AssembleWithProvider(cfg, prov)
	if err != nil {
		t.Fatal(err)
	}
	return app.ToolContextForSubject("user:alice", nil)
}

func call(ctx context.Context, tc tool.Context, fields map[string]any) (tool.Result, error) {
	in := map[string]any{"datasource": "default"}
	for k, v := range fields {
		in[k] = v
	}
	raw, _ := json.Marshal(in)
	return tool.RunTool(ctx, tool.ExecuteSQLTool{}, raw, tc)
}

func run(t *testing.T, ctx context.Context, tc tool.Context, fields map[string]any) map[string]any {
	t.Helper()
	res, err := call(ctx, tc, fields)
	if err != nil {
		t.Fatalf("%v: %v", fields["sql"], err)
	}
	return res.Content[0]
}
