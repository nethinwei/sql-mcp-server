//go:build integration

package mysql_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/core/tool"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
)

// A view is discovered without MySQL's "VIEW" placeholder comment, a view
// entity assembles and serves reads, and the server reports its identity.
func TestMySQLViewsAndIdentity(t *testing.T) {
	prov, cleanup := setupMySQL(t)
	defer cleanup()
	ctx := context.Background()
	if _, err := prov.ExecContext(ctx,
		`CREATE VIEW active_users AS SELECT id, email FROM users WHERE tenant_id = 7`); err != nil {
		t.Fatal(err)
	}
	found, err := prov.Introspector().Discover(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var view entity.Entity
	for _, e := range found {
		if e.Name == "active_users" {
			view = e
		}
	}
	if view.Kind != entity.KindView || !view.Derived || view.Description != "" {
		t.Fatalf("view = %+v", view)
	}
	physical, err := prov.Introspector().(introspect.PhysicalNamer).Physical(ctx)
	if err != nil || physical.Server == "" || physical.Catalog != "" {
		t.Fatalf("physical = %+v, %v", physical, err)
	}
	cfg := &config.Config{
		Database: config.DatabaseConfig{Driver: "mysql", DSN: "ignored"},
		Cost:     config.CostConfig{Enabled: new(false)},
		Entities: []config.EntityConfig{{
			Name: "active_users", Kind: "view", Roles: config.RoleConfig{Read: []string{"reader"}},
			Fields: []config.FieldConfig{{Name: "id"}, {Name: "email"}},
		}},
	}
	cfg.ApplyDefaults()
	app, err := bootstrap.AssembleWithProvider(cfg, prov)
	if err != nil {
		t.Fatalf("view entity must assemble: %v", err)
	}
	in, _ := json.Marshal(map[string]any{"entity": "active_users"})
	res, err := tool.ReadTool{}.Run(ctx, in, app.ToolContext("reader"))
	if err != nil || len(res.Content) != 1 || res.Content[0]["email"] != "alice@x.com" {
		t.Fatalf("read view: %+v, %v", res.Content, err)
	}
}
