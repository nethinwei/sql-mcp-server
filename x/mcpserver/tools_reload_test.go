package mcpserver_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/mcpserver"
)

func toolsConfig(delete bool) *config.Config {
	cfg := &config.Config{
		Server:   config.ServerConfig{Role: "reader"},
		Database: config.DatabaseConfig{Driver: "postgres", DSN: "ignored"},
		Entities: []config.EntityConfig{{
			Name: "users", PrimaryKey: []string{"id"}, Roles: config.RoleConfig{Read: []string{"reader"}},
			Fields: []config.FieldConfig{{Name: "id"}},
		}},
	}
	cfg.ApplyDefaults()
	cfg.Tools.DeleteRecord = delete
	return cfg
}

func listedToolNames(ctx context.Context, t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()
	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	return names
}

// A reload that changes the tool set changes what the server lists, without
// a restart, and tells connected clients the list changed.
func TestReloadChangesListedTools(t *testing.T) {
	t.Parallel()
	assemble := func(delete bool) *bootstrap.App {
		app, err := bootstrap.AssembleWithProvider(toolsConfig(delete), &instantProvider{})
		if err != nil {
			t.Fatal(err)
		}
		return app
	}
	runtime := bootstrap.NewRuntimeWithBuilder(assemble(true), func(string) (*bootstrap.App, error) {
		return assemble(false), nil
	})
	t.Cleanup(func() { _ = runtime.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	server, client := mcp.NewInMemoryTransports()
	go func() { _ = mcpserver.NewRuntimeServer(runtime).Run(ctx, server) }()
	changed := make(chan struct{}, 1)
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
			select {
			case changed <- struct{}{}:
			default:
			}
		},
	}).Connect(ctx, client, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if !slices.Contains(listedToolNames(ctx, t, session), "delete_record") {
		t.Fatal("delete_record must be listed while enabled")
	}
	if err := runtime.Reload("ignored"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("the client was not told the tool list changed")
	}
	if names := listedToolNames(ctx, t, session); slices.Contains(names, "delete_record") ||
		!slices.Contains(names, "read_records") {
		t.Fatalf("tools after the reload = %v", names)
	}
}
