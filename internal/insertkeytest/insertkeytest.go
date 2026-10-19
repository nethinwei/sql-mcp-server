// Package insertkeytest checks, against a real database without RETURNING
// (MySQL, OceanBase), that create_record reports the key the row was stored
// under, the same way for every such provider: an auto-increment key the
// caller leaves out, writes as NULL or 0 (the database generates it), or
// writes as a value of its own, under the key's alias.
package insertkeytest

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/tool"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
)

// Run creates schema.tickets (schema may be empty) on prov, driver's, and
// creates its rows through create_record.
func Run(t *testing.T, driver string, prov bootstrap.Provider, schema string) {
	t.Helper()
	ctx := context.Background()
	table := "tickets"
	if schema != "" {
		table = schema + ".tickets"
	}
	if _, err := prov.ExecContext(ctx, "CREATE TABLE "+table+
		" (id INT AUTO_INCREMENT PRIMARY KEY, code VARCHAR(20) NOT NULL UNIQUE)"); err != nil {
		t.Fatal(err)
	}
	tc := assemble(t, driver, prov, schema)
	for _, c := range []struct{ code, key string }{
		{"omitted", ""}, {"null", `,"key":null`}, {"zero", `,"key":0`}, {"explicit", `,"key":500`},
		{"after", ""},
	} {
		res, err := tool.RunTool(ctx, tool.CreateTool{},
			json.RawMessage(`{"entity":"tickets","values":{"code":"`+c.code+`"`+c.key+`}}`), tc)
		if err != nil {
			t.Fatalf("%s: %v", c.code, err)
		}
		if got, want := fmt.Sprint(res.Content[0]["id"]), stored(t, ctx, prov, table, c.code); got != want {
			t.Errorf("%s: create reported id %s (%v), the row is stored under %s", c.code, got, res.Content[0], want)
		}
	}
}

func stored(t *testing.T, ctx context.Context, prov bootstrap.Provider, table, code string) string {
	t.Helper()
	rows, err := prov.QueryContext(ctx, "SELECT id FROM "+table+" WHERE code = ?", code)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var id int64
	if !rows.Next() {
		t.Fatalf("%s: no row stored", code)
	}
	if err := rows.Scan(&id); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprint(id)
}

func assemble(t *testing.T, driver string, prov bootstrap.Provider, schema string) tool.Context {
	t.Helper()
	off := false
	cfg := &config.Config{
		Server:   config.ServerConfig{Role: "clerk"},
		Database: config.DatabaseConfig{Driver: driver, DSN: "ignored"},
		Entities: []config.EntityConfig{{
			Name: "tickets", Schema: schema, PrimaryKey: []string{"id"},
			Fields: []config.FieldConfig{{Name: "id", Alias: "key"}, {Name: "code"}},
			Roles:  config.RoleConfig{Read: []string{"clerk"}, Create: []string{"clerk"}},
			MCP:    config.MCPFlags{DMLTools: true},
		}},
		Tools: config.DefaultToolFlags(),
		Cost:  config.CostConfig{Enabled: &off}, // the plans of a fresh table are not the point
	}
	cfg.Tools.CreateRecord = true
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
