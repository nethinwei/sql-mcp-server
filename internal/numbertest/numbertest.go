// Package numbertest writes and filters JSON numbers against a real
// database, the same way for every provider: a decimal keeps every digit, a
// double compares as a float, and a fraction for an integer column is
// refused before it reaches the database.
package numbertest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/tool"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
)

// exact is past what a float64 holds: 17 significant digits and cents.
const exact = "12345678901234567.89"

// Run creates schema.ledger (schema may be empty) on prov, driver's, and
// writes and filters its numbers through the governed tools.
func Run(t *testing.T, driver string, prov bootstrap.Provider, schema string) {
	t.Helper()
	ctx := context.Background()
	table := "ledger"
	if schema != "" {
		table = schema + ".ledger"
	}
	if _, err := prov.ExecContext(ctx, "CREATE TABLE "+table+
		" (id INT PRIMARY KEY, amount NUMERIC(30,2), ratio DOUBLE PRECISION, qty INT)"); err != nil {
		t.Fatal(err)
	}
	tc := assemble(t, driver, prov, schema)
	call(t, ctx, tc, tool.CreateTool{}, `{"entity":"ledger","values":{"id":1,"amount":`+exact+
		`,"ratio":0.5,"qty":3}}`)
	rows := read(t, ctx, tc, `[{"field":"amount","op":"eq","value":`+exact+`}]`)
	if len(rows) != 1 || !strings.HasPrefix(fmt.Sprint(rows[0]["amount"]), exact) {
		t.Fatalf("decimal filter = %v", rows)
	}
	if rows := read(t, ctx, tc, `[{"field":"ratio","op":"gt","value":0.25}]`); len(rows) != 1 {
		t.Fatalf("double filter = %v", rows)
	}
	call(t, ctx, tc, tool.UpdateTool{},
		`{"entity":"ledger","filter":[{"field":"id","op":"eq","value":1}],"set":{"amount":0.10}}`)
	if rows := read(t, ctx, tc, `[{"field":"amount","op":"in","value":[0.1,2.5]}]`); len(rows) != 1 {
		t.Fatalf("decimal IN after the update = %v", rows)
	}
	_, err := tool.RunTool(ctx, tool.CreateTool{},
		json.RawMessage(`{"entity":"ledger","values":{"id":2,"qty":1.5}}`), tc)
	if !errors.Is(err, tool.ErrInvalidInput) {
		t.Fatalf("a fraction for an integer column: %v", err)
	}
}

func assemble(t *testing.T, driver string, prov bootstrap.Provider, schema string) tool.Context {
	t.Helper()
	off := false
	cfg := &config.Config{
		Server:   config.ServerConfig{Role: "clerk"},
		Database: config.DatabaseConfig{Driver: driver, DSN: "ignored"},
		Entities: []config.EntityConfig{{
			Name: "ledger", Schema: schema, PrimaryKey: []string{"id"},
			Fields: []config.FieldConfig{{Name: "id"}, {Name: "amount"}, {Name: "ratio"}, {Name: "qty"}},
			Roles: config.RoleConfig{
				Read: []string{"clerk"}, Create: []string{"clerk"}, Update: []string{"clerk"},
			},
			MCP: config.MCPFlags{DMLTools: true},
		}},
		Tools: config.DefaultToolFlags(),
		Cost:  config.CostConfig{Enabled: &off}, // the plans of a fresh table are not the point
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	app, err := bootstrap.AssembleWithProvider(cfg, prov)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	return app.ToolContext("clerk")
}

func call(t *testing.T, ctx context.Context, tc tool.Context, tl tool.Tool, input string) tool.Result {
	t.Helper()
	res, err := tool.RunTool(ctx, tl, json.RawMessage(input), tc)
	if err != nil {
		t.Fatalf("%s: %v", input, err)
	}
	return res
}

func read(t *testing.T, ctx context.Context, tc tool.Context, filter string) []map[string]any {
	t.Helper()
	return call(t, ctx, tc, tool.ReadTool{}, `{"entity":"ledger","filter":`+filter+`}`).Content
}
