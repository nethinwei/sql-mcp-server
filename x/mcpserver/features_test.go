package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nethinwei/sql-mcp-server/config"
	"github.com/nethinwei/sql-mcp-server/entity"
	"github.com/nethinwei/sql-mcp-server/rbac"
	"github.com/nethinwei/sql-mcp-server/store"
	"github.com/nethinwei/sql-mcp-server/tool"
	"github.com/nethinwei/sql-mcp-server/version"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

func TestAuthorizedSchemaResourceAndPrompts(t *testing.T) {
	registry, err := entity.NewRegistry([]entity.Entity{
		{
			Name: "orders", MCP: entity.MCPFlags{DMLTools: true},
			Attributes: []entity.Attribute{{Name: "id"}, {Name: "secret"}},
			Role: entity.RoleAccess{
				entity.ActionRead:      {"reader"},
				entity.ActionAggregate: {"reader"},
			},
			FieldAccess: entity.FieldAccess{
				"reader": {Read: []string{"id"}},
			},
			RowPolicies: entity.RowPolicies{"reader": nil},
		},
		{
			Name: "admin_only", MCP: entity.MCPFlags{DMLTools: true},
			Attributes: []entity.Attribute{{Name: "id"}},
			Role:       entity.RoleAccess{entity.ActionRead: {"admin"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := tool.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	app := &bootstrap.App{
		Registry: registry, Authorizer: rbac.NewRoleAuthorizer(registry),
		Tools: tools, DefaultRole: "reader",
	}
	server := NewServer(app)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if got := session.InitializeResult().ServerInfo.Version; got != version.String() {
		t.Fatalf("server version = %q, want %q", got, version.String())
	}

	resources, err := session.ListResources(ctx, &mcp.ListResourcesParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resources.Resources) != 1 || resources.Resources[0].URI != schemaResourceURI {
		t.Fatalf("resources = %+v", resources.Resources)
	}
	result, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: schemaResourceURI})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Contents) != 1 {
		t.Fatalf("contents = %+v", result.Contents)
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(result.Contents[0].Text), &schema); err != nil {
		t.Fatal(err)
	}
	text := result.Contents[0].Text
	if !strings.Contains(text, `"orders"`) || !strings.Contains(text, `"id"`) {
		t.Fatalf("authorized schema missing visible fields: %s", text)
	}
	if strings.Contains(text, `"secret"`) || strings.Contains(text, `"admin_only"`) {
		t.Fatalf("authorized schema leaked inaccessible metadata: %s", text)
	}

	prompts, err := session.ListPrompts(ctx, &mcp.ListPromptsParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(prompts.Prompts) != 3 {
		t.Fatalf("got %d prompts, want 3", len(prompts.Prompts))
	}
	prompt, err := session.GetPrompt(ctx, &mcp.GetPromptParams{
		Name: "safe_read", Arguments: map[string]string{"request": "find order 1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	content, ok := prompt.Messages[0].Content.(*mcp.TextContent)
	if !ok || !strings.Contains(content.Text, "authorized-schema") || !strings.Contains(content.Text, "find order 1") {
		t.Fatalf("prompt = %+v", prompt.Messages)
	}
}

func TestCustomProcedureThroughMCP(t *testing.T) {
	procedure := entity.Entity{
		Name: "refresh-cache", Source: "refresh_cache", Kind: entity.KindProcedure,
		Params: []string{"tenant"}, Role: entity.RoleAccess{entity.ActionExecute: {"caller"}},
		MCP: entity.MCPFlags{CustomTool: true, TrustedProcedure: true}, DataSource: "default",
	}
	registry, err := entity.NewRegistry([]entity.Entity{procedure})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := tool.NewRegistry(tool.DefaultTools())
	if err != nil {
		t.Fatal(err)
	}
	db := &store.FakeDB{QueryFn: func(_ context.Context, _ string, args ...any) (store.Rows, error) {
		if len(args) != 1 || args[0] != "acme" {
			t.Fatalf("procedure args = %v", args)
		}
		return store.NewFakeRows([]string{"ok"}, []any{true}), nil
	}}
	app := &bootstrap.App{
		Sources: map[string]tool.DataSource{
			"default": {DB: db, Dialect: postgres.Dialect{}},
		},
		Registry: registry, Authorizer: rbac.NewRoleAuthorizer(registry),
		Tools: tools, ToolFlags: config.DefaultToolFlags(), DefaultRole: "caller",
	}
	server := NewServer(app)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: tool.ProcedureToolName(procedure.Name), Arguments: map[string]any{"tenant": "acme"},
	})
	if err != nil || result.IsError {
		t.Fatalf("custom procedure result = %+v, error = %v", result, err)
	}
}
