package mcpserver_test

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/mcpserver"
	pgprov "github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

// instantProvider answers every query at once with one row, so the
// benchmark measures the MCP protocol and the governed tool path.
type instantProvider struct{ store.FakeDB }

func (*instantProvider) Dialect() dialect.Dialect { return pgprov.Dialect{} }
func (*instantProvider) Explainer() cost.Explainer {
	return cost.FakeExplainer{Plan: cost.Plan{ScanType: cost.ScanIndex, EstimatedRows: 1, StatsFresh: true}}
}
func (*instantProvider) Introspector() introspect.Introspector { return nil }
func (*instantProvider) Close() error                          { return nil }
func (*instantProvider) QueryContext(context.Context, string, ...any) (store.Rows, error) {
	return store.NewFakeRows([]string{"id", "email"}, []any{int64(1), "alice@x.com"}), nil
}

// BenchmarkMCPPointRead measures one tools/call of read_records over an
// in-memory MCP session: JSON-RPC, tool dispatch and the governed read.
func BenchmarkMCPPointRead(b *testing.B) {
	cfg := &config.Config{
		Server:   config.ServerConfig{Role: "reader"},
		Database: config.DatabaseConfig{Driver: "postgres", DSN: "ignored"},
		Entities: []config.EntityConfig{{
			Name: "users", PrimaryKey: []string{"id"}, Roles: config.RoleConfig{Read: []string{"reader"}},
			Fields: []config.FieldConfig{{Name: "id"}, {Name: "email", Mask: "email"}},
		}},
	}
	cfg.ApplyDefaults()
	app, err := bootstrap.AssembleWithProvider(cfg, &instantProvider{})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = app.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	b.Cleanup(cancel)
	server, client := mcp.NewInMemoryTransports()
	go func() { _ = mcpserver.NewServer(app).Run(ctx, server) }()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "bench"}, nil).Connect(ctx, client, nil)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = session.Close() })
	params := &mcp.CallToolParams{Name: "read_records", Arguments: map[string]any{
		"entity": "users", "filter": []any{map[string]any{"field": "id", "op": "eq", "value": 1}},
	}}
	b.ReportAllocs()
	for b.Loop() {
		res, err := session.CallTool(ctx, params)
		if err != nil || res.IsError {
			b.Fatalf("call: %v %+v", err, res)
		}
	}
}
