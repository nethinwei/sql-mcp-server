//go:build integration

package oceanbase_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/nethinwei/sql-mcp-server/config"
	"github.com/nethinwei/sql-mcp-server/cost"
	"github.com/nethinwei/sql-mcp-server/entity"
	"github.com/nethinwei/sql-mcp-server/store"
	"github.com/nethinwei/sql-mcp-server/tool"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/providers/oceanbase"
)

func setupOB(t *testing.T) (*oceanbase.Provider, func()) {
	t.Helper()
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "oceanbase/oceanbase-ce:latest",
			ExposedPorts: []string{"2881/tcp"},
			WaitingFor:   wait.ForListeningPort("2881/tcp").WithStartupTimeout(5 * time.Minute),
			Env:          map[string]string{"MODE": "mini"},
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start oceanbase container: %v", err)
	}
	host, _ := container.Host(ctx)
	port, _ := container.MappedPort(ctx, "2881")
	dsn := fmt.Sprintf("root:@tcp(%s:%s)/?parseTime=true", host, port.Port())

	// OceanBase takes time to become query-ready after the port is open; retry.
	var prov *oceanbase.Provider
	for i := 0; i < 40; i++ {
		prov, err = oceanbase.New(dsn)
		if err == nil {
			break
		}
		time.Sleep(5 * time.Second)
	}
	if err != nil {
		_ = container.Terminate(context.Background())
		t.Fatalf("connect oceanbase: %v", err)
	}
	// Retry DDL until the tenant accepts writes.
	for i := 0; i < 40; i++ {
		_, e1 := prov.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS test")
		_, e2 := prov.ExecContext(ctx,
			"CREATE TABLE IF NOT EXISTS test.users (id int AUTO_INCREMENT PRIMARY KEY, email text, tenant_id int)")
		if e1 == nil && e2 == nil {
			break
		}
		time.Sleep(5 * time.Second)
	}
	// Idempotent data: clear then insert, retried until the tenant is ready.
	for i := 0; i < 40; i++ {
		_, _ = prov.ExecContext(ctx, "DELETE FROM test.users")
		_, e := prov.ExecContext(ctx,
			"INSERT INTO test.users (email, tenant_id) VALUES ('alice@x.com', 7), ('bob@x.com', 8)")
		if e == nil {
			break
		}
		time.Sleep(5 * time.Second)
	}
	return prov, func() {
		_ = prov.Close()
		_ = container.Terminate(context.Background())
	}
}

func TestOBProviderQueryExecExplainIntrospect(t *testing.T) {
	prov, cleanup := setupOB(t)
	defer cleanup()
	ctx := context.Background()

	rows, err := prov.QueryContext(ctx, "SELECT id, email FROM test.users ORDER BY id")
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

	plan, err := prov.Explainer().Explain(ctx, "SELECT * FROM test.users", nil)
	if err != nil {
		t.Fatalf("explain failed: %v", err)
	}
	t.Logf("OB explain plan: ScanType=%v rows=%d cost=%v raw=%s",
		plan.ScanType, plan.EstimatedRows, plan.TotalCost, string(plan.Raw))
	if plan.ScanType == cost.ScanUnknown {
		t.Fatalf("expected a known scan type, got ScanUnknown (raw=%s)", string(plan.Raw))
	}

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

func TestOBReadEnforceCap(t *testing.T) {
	prov, cleanup := setupOB(t)
	defer cleanup()
	ctx := context.Background()
	cfg := &config.Config{
		Server:   config.ServerConfig{Role: "reader"},
		Database: config.DatabaseConfig{Driver: "oceanbase", DSN: "ignored"},
		Entities: []config.EntityConfig{{
			Name: "users", Source: "users", Schema: "test", Kind: "table", PrimaryKey: []string{"id"},
			Fields: []config.FieldConfig{{Name: "id"}, {Name: "email"}},
			Roles:  config.RoleConfig{Read: []string{"reader"}},
		}},
		Tools: config.DefaultToolFlags(),
		Cost:  config.CostConfig{Enabled: true, SoftScore: 90, HardScore: 95, MaxRows: 1},
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

func TestOBRLSRowFilterAndMasking(t *testing.T) {
	prov, cleanup := setupOB(t)
	defer cleanup()
	ctx := context.Background()
	cfg := &config.Config{
		Server:   config.ServerConfig{Role: "reader"},
		Database: config.DatabaseConfig{Driver: "oceanbase", DSN: "ignored"},
		Entities: []config.EntityConfig{{
			Name: "users", Source: "users", Schema: "test", Kind: "table", PrimaryKey: []string{"id"},
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

func TestOBExecuteProcedure(t *testing.T) {
	prov, cleanup := setupOB(t)
	defer cleanup()
	ctx := context.Background()
	_, _ = prov.ExecContext(ctx, "CREATE PROCEDURE count_users() BEGIN SELECT count(*) AS n FROM test.users; END")
	cfg := &config.Config{
		Server:   config.ServerConfig{Role: "caller"},
		Database: config.DatabaseConfig{Driver: "oceanbase", DSN: "ignored"},
		Entities: []config.EntityConfig{{
			Name: "count_users", Source: "count_users", Kind: "procedure",
			Roles: config.RoleConfig{Execute: []string{"caller"}},
		}},
		Tools: config.DefaultToolFlags(),
		Cost:  config.CostConfig{Enabled: false},
	}
	cfg.ApplyDefaults()
	app, err := bootstrap.AssembleWithProvider(cfg, prov)
	if err != nil {
		t.Fatal(err)
	}
	tc := app.ToolContext("caller")
	in, _ := json.Marshal(map[string]any{"entity": "count_users"})
	res, err := tool.ExecuteTool{}.Run(ctx, in, tc)
	if err != nil {
		t.Fatalf("execute should succeed, got %v", err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("expected 1 row, got %v", res.Content)
	}
}

func TestOBUpdateUnsafeWriteAndPK(t *testing.T) {
	prov, cleanup := setupOB(t)
	defer cleanup()
	ctx := context.Background()
	cfg := &config.Config{
		Server:   config.ServerConfig{Role: "writer"},
		Database: config.DatabaseConfig{Driver: "oceanbase", DSN: "ignored"},
		Entities: []config.EntityConfig{{
			Name: "users", Source: "users", Schema: "test", Kind: "table", PrimaryKey: []string{"id"},
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
