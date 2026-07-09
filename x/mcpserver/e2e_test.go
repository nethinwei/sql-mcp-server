//go:build e2e

package mcpserver_test

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"go.uber.org/goleak"

	"github.com/nethinwei/sql-mcp-server/config"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/mcpserver"
	pgprov "github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

// TestMain verifies no goroutine leaks across the e2e suite.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func setupApp(t *testing.T) (*bootstrap.App, func()) {
	t.Helper()
	ctx := context.Background()
	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	prov, err := pgprov.New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = prov.ExecContext(ctx, "CREATE TABLE users (id serial PRIMARY KEY, email text)")
	_, _ = prov.ExecContext(ctx, "INSERT INTO users (email) VALUES ('alice@x.com')")
	cfg := &config.Config{
		Server:   config.ServerConfig{Role: "reader"},
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
	return app, func() {
		_ = app.Close()
		_ = container.Terminate(context.Background())
	}
}

func TestE2EListToolsAndCall(t *testing.T) {
	app, cleanup := setupApp(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := mcpserver.NewServer(app)
	stServer, stClient := mcp.NewInMemoryTransports()
	go func() { _ = srv.Run(ctx, stServer) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "e2e-test"}, nil)
	session, err := client.Connect(ctx, stClient, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	// ListTools: read_records present, delete_record absent (default off).
	lt, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool, len(lt.Tools))
	for _, tl := range lt.Tools {
		names[tl.Name] = true
	}
	if !names["read_records"] {
		t.Fatal("read_records not registered")
	}
	if names["delete_record"] {
		t.Fatal("delete_record should be off by default")
	}

	// read_records with a PK point lookup: succeeds and returns the row.
	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "read_records",
		Arguments: map[string]any{
			"entity": "users",
			"filter": []map[string]any{{"field": "id", "op": "eq", "value": 1}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("PK lookup should succeed: %+v", res)
	}
	if !contentContains(res, "alice") {
		t.Fatalf("expected 'alice' in result, got %+v", res.Content)
	}

	// read_records with no filter (full scan): rejected as IsError by the gate.
	res2, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "read_records",
		Arguments: map[string]any{"entity": "users"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res2.IsError {
		t.Fatal("full scan should be rejected with IsError")
	}
}

func contentContains(res *mcp.CallToolResult, want string) bool {
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok && strings.Contains(tc.Text, want) {
			return true
		}
	}
	return false
}
