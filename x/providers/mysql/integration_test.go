//go:build integration

package mysql_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"

	"github.com/nethinwei/sql-mcp-server/config"
	"github.com/nethinwei/sql-mcp-server/cost"
	"github.com/nethinwei/sql-mcp-server/entity"
	"github.com/nethinwei/sql-mcp-server/store"
	"github.com/nethinwei/sql-mcp-server/tool"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/providers/mysql"
)

func setupMySQL(t *testing.T) (*mysql.Provider, func()) {
	t.Helper()
	ctx := context.Background()
	container, err := tcmysql.Run(ctx, "mysql:8",
		tcmysql.WithDatabase("test"),
		tcmysql.WithUsername("test"),
		tcmysql.WithPassword("test"),
	)
	if err != nil {
		t.Fatalf("start mysql container: %v", err)
	}
	dsn, err := container.ConnectionString(ctx, "parseTime=true")
	if err != nil {
		t.Fatal(err)
	}
	prov, err := mysql.New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = prov.ExecContext(ctx,
		"CREATE TABLE users (id int AUTO_INCREMENT PRIMARY KEY, email text, tenant_id int)")
	_, _ = prov.ExecContext(ctx,
		"INSERT INTO users (email, tenant_id) VALUES ('alice@x.com', 7), ('bob@x.com', 8)")
	return prov, func() {
		_ = prov.Close()
		_ = container.Terminate(context.Background())
	}
}

func TestMySQLProviderQueryExecExplainIntrospect(t *testing.T) {
	prov, cleanup := setupMySQL(t)
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

	// EXPLAIN: an unfiltered select is access_type ALL -> ScanFull.
	plan, err := prov.Explainer().Explain(ctx, "SELECT * FROM users", nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ScanType != cost.ScanFull {
		t.Fatalf("ScanType = %v, want ScanFull for unfiltered scan", plan.ScanType)
	}

	// Introspect: discover the users table with a primary key.
	entities, err := prov.Introspector().Discover(ctx, nil)
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

func TestMySQLReadEnforceCap(t *testing.T) {
	prov, cleanup := setupMySQL(t)
	defer cleanup()
	ctx := context.Background()
	cfg := &config.Config{
		Server:   config.ServerConfig{Role: "reader"},
		Database: config.DatabaseConfig{Driver: "mysql", DSN: "ignored"},
		Entities: []config.EntityConfig{{
			Name: "users", Source: "users", Kind: "table", PrimaryKey: []string{"id"},
			Fields: []config.FieldConfig{{Name: "id"}, {Name: "email"}},
			Roles:  config.RoleConfig{Read: []string{"reader"}},
		}},
		Tools: config.DefaultToolFlags(),
		// MySQL estimates are not trusted (ExplainAccurate=false), so the gate
		// skips Estimate and relies on EnforceCap to bound rows with LIMIT.
		Cost: config.CostConfig{Enabled: true, SoftScore: 90, HardScore: 95, MaxRows: 1},
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

func TestMySQLRLSRowFilterAndMasking(t *testing.T) {
	prov, cleanup := setupMySQL(t)
	defer cleanup()
	ctx := context.Background()
	cfg := &config.Config{
		Server:   config.ServerConfig{Role: "reader"},
		Database: config.DatabaseConfig{Driver: "mysql", DSN: "ignored"},
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
		Cost:  config.CostConfig{Enabled: true, SoftScore: 40, HardScore: 70, MaxRows: 10000, WhitelistPKPoint: true},
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
		t.Fatalf("read should pass, got %v", err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("RLS should limit to 1 row (tenant 7), got %d", len(res.Content))
	}
	if res.Content[0]["email"] != "a***@x.com" {
		t.Fatalf("email not masked: %v", res.Content[0]["email"])
	}
}

func TestMySQLUpdateUnsafeWriteAndPK(t *testing.T) {
	prov, cleanup := setupMySQL(t)
	defer cleanup()
	ctx := context.Background()
	cfg := &config.Config{
		Server:   config.ServerConfig{Role: "writer"},
		Database: config.DatabaseConfig{Driver: "mysql", DSN: "ignored"},
		Entities: []config.EntityConfig{{
			Name: "users", Source: "users", Kind: "table", PrimaryKey: []string{"id"},
			Fields: []config.FieldConfig{{Name: "id"}, {Name: "email"}},
			Roles:  config.RoleConfig{Update: []string{"writer"}},
		}},
		Tools: config.DefaultToolFlags(),
		Cost:  config.CostConfig{Enabled: false},
	}
	cfg.ApplyDefaults()
	app, err := bootstrap.AssembleWithProvider(cfg, prov)
	if err != nil {
		t.Fatal(err)
	}
	tc := app.ToolContext("writer")

	unsafe, _ := json.Marshal(map[string]any{"entity": "users", "set": map[string]any{"email": "x@x.com"}})
	_, err = tool.UpdateTool{}.Run(ctx, unsafe, tc)
	if !errors.Is(err, tool.ErrUnsafeWrite) {
		t.Fatalf("expected ErrUnsafeWrite, got %v", err)
	}

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

func TestMySQLReadPKWhitelist(t *testing.T) {
	prov, cleanup := setupMySQL(t)
	defer cleanup()
	ctx := context.Background()
	cfg := &config.Config{
		Server:   config.ServerConfig{Role: "reader"},
		Database: config.DatabaseConfig{Driver: "mysql", DSN: "ignored"},
		Entities: []config.EntityConfig{{
			Name: "users", Source: "users", Kind: "table", PrimaryKey: []string{"id"},
			Fields: []config.FieldConfig{{Name: "id"}, {Name: "email"}},
			Roles:  config.RoleConfig{Read: []string{"reader"}},
		}},
		Tools: config.DefaultToolFlags(),
		Cost:  config.CostConfig{Enabled: true, SoftScore: 40, HardScore: 70, MaxRows: 10000, WhitelistPKPoint: true},
	}
	cfg.ApplyDefaults()
	app, err := bootstrap.AssembleWithProvider(cfg, prov)
	if err != nil {
		t.Fatal(err)
	}
	tc := app.ToolContext("reader")

	pkLookup, _ := json.Marshal(map[string]any{
		"entity": "users",
		"filter": []map[string]any{{"field": "id", "op": "eq", "value": 1}},
	})
	res, err := tool.ReadTool{}.Run(ctx, pkLookup, tc)
	if err != nil {
		t.Fatalf("PK lookup should pass, got %v", err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("expected 1 row, got %d", len(res.Content))
	}
}
