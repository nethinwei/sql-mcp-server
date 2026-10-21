package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/core/store/storetest"
	"github.com/nethinwei/sql-mcp-server/core/tool"
	"github.com/nethinwei/sql-mcp-server/version"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

func TestAuthorizedSchemaResourceAndPrompts(t *testing.T) {
	app := authorizedSchemaTestApp(t)
	session, ctx, cancel := connectMCPSession(t, app)
	defer cancel()
	defer session.Close()

	assertServerVersion(t, session)
	assertAuthorizedSchemaResource(t, ctx, session)
	assertAuthorizedSchemaPrompts(t, ctx, session)
}

func authorizedSchemaTestApp(t *testing.T) *bootstrap.App {
	t.Helper()
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
	return &bootstrap.App{
		Registry: registry, Authorizer: rbac.NewRoleAuthorizer(registry),
		Tools: tools, DefaultRole: "reader",
	}
}

func connectMCPSession(t *testing.T, app *bootstrap.App) (*mcp.ClientSession, context.Context, context.CancelFunc) {
	t.Helper()
	server := NewServer(app)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = server.Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	return session, ctx, cancel
}

func assertServerVersion(t *testing.T, session *mcp.ClientSession) {
	t.Helper()
	if got := session.InitializeResult().ServerInfo.Version; got != version.String() {
		t.Fatalf("server version = %q, want %q", got, version.String())
	}
}

func assertAuthorizedSchemaResource(t *testing.T, ctx context.Context, session *mcp.ClientSession) {
	t.Helper()
	resources, err := session.ListResources(ctx, &mcp.ListResourcesParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resources.Resources) != 1 || resources.Resources[0].URI != schemaResourceURI {
		t.Fatalf("resources = %+v", resources.Resources)
	}
	templates, err := session.ListResourceTemplates(ctx, &mcp.ListResourceTemplatesParams{})
	if err != nil || len(templates.ResourceTemplates) != 1 ||
		templates.ResourceTemplates[0].URITemplate != schemaEntityTemplate {
		t.Fatalf("templates = %+v, %v", templates, err)
	}
	read := func(uri string) (string, error) {
		result, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
		if err != nil {
			return "", err
		}
		if len(result.Contents) != 1 || !json.Valid([]byte(result.Contents[0].Text)) {
			t.Fatalf("contents = %+v", result.Contents)
		}
		return result.Contents[0].Text, nil
	}
	summary, err := read(schemaResourceURI)
	if err != nil || !strings.Contains(summary, `"entities":1`) ||
		!strings.Contains(summary, `"datasources":[{"entities":1,"name":"default"}]`) {
		t.Fatalf("summary = %s, %v", summary, err)
	}
	text, err := read(schemaEntityPrefix + "orders")
	if err != nil || !strings.Contains(text, `"orders"`) || !strings.Contains(text, `"id"`) {
		t.Fatalf("authorized schema missing visible fields: %s, %v", text, err)
	}
	if strings.Contains(summary+text, `"secret"`) || strings.Contains(summary+text, `"admin_only"`) {
		t.Fatalf("authorized schema leaked inaccessible metadata: %s %s", summary, text)
	}
	// An entity the caller may not read is as absent as one that does not exist.
	for _, ref := range []string{"admin_only", "missing"} {
		if _, err := read(schemaEntityPrefix + ref); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("%s: err = %v", ref, err)
		}
	}
}

func assertAuthorizedSchemaPrompts(t *testing.T, ctx context.Context, session *mcp.ClientSession) {
	t.Helper()
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
	if !ok || !strings.Contains(content.Text, "describe_entities") || !strings.Contains(content.Text, "find order 1") {
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
	db := &storetest.FakeDB{QueryFn: func(_ context.Context, _ string, args ...any) (store.Rows, error) {
		if len(args) != 1 || args[0] != "acme" {
			t.Fatalf("procedure args = %v", args)
		}
		return storetest.NewFakeRows([]string{"ok"}, []any{true}), nil
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
		Name: "procedure_refresh-cache", Arguments: map[string]any{"tenant": "acme"},
	})
	if err != nil || result.IsError {
		t.Fatalf("custom procedure result = %+v, error = %v", result, err)
	}
}

// Two read grants with incompatible field scopes deny the default projection,
// but the entity stays discoverable with the scopes to choose fields from.
func TestAuthorizedSchemaListsSplitFieldScopes(t *testing.T) {
	t.Parallel()
	registry, err := entity.NewRegistry([]entity.Entity{{
		Name: "customers", MCP: entity.MCPFlags{DMLTools: true},
		Attributes: []entity.Attribute{{Name: "id"}, {Name: "name"}, {Name: "phone"}, {Name: "secret"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	read := func(id string, fields ...string) rbac.Grant {
		return rbac.Grant{ID: id, Actions: []entity.Action{entity.ActionRead},
			Fields: &entity.FieldPermissions{Read: fields}}
	}
	policy := rbac.Policy{Roles: map[string]map[string][]rbac.Grant{
		"reader": {"customers": {read("n", "id", "name"), read("p", "id", "phone")}},
	}}
	app := &bootstrap.App{Registry: registry, Authorizer: rbac.NewGrantAuthorizer(registry, policy)}
	schema, ok, err := entitySchema(context.Background(), app, "reader", nil, "customers")
	if err != nil || !ok {
		t.Fatal(err)
	}
	data, _ := json.Marshal(schema)
	text := string(data)
	for _, want := range []string{`"customers"`,
		`"read":{"explicitFieldsRequired":true,"fieldScopes":[["id","name"],["id","phone"]],"fields":["id","name","phone"]}`,
		`"actions":["read"]`} {
		if !strings.Contains(text, want) {
			t.Fatalf("schema missing %s: %s", want, text)
		}
	}
	if strings.Contains(text, `"secret"`) {
		t.Fatalf("schema leaked an unreadable field: %s", text)
	}
}

// Read and aggregate grants cover different fields: each action reports its
// own fields and scopes instead of the read ones standing for both.
func TestAuthorizedSchemaReportsFieldsPerAction(t *testing.T) {
	t.Parallel()
	registry, err := entity.NewRegistry([]entity.Entity{{
		Name: "orders", MCP: entity.MCPFlags{DMLTools: true},
		Attributes: []entity.Attribute{{Name: "id"}, {Name: "amount"}, {Name: "region"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	grant := func(id string, action entity.Action, fields ...string) rbac.Grant {
		return rbac.Grant{ID: id, Actions: []entity.Action{action}, Fields: &entity.FieldPermissions{Read: fields}}
	}
	policy := rbac.Policy{Roles: map[string]map[string][]rbac.Grant{"analyst": {"orders": {
		grant("r", entity.ActionRead, "id"),
		grant("a1", entity.ActionAggregate, "id", "amount"),
		grant("a2", entity.ActionAggregate, "id", "region"),
	}}}}
	app := &bootstrap.App{Registry: registry, Authorizer: rbac.NewGrantAuthorizer(registry, policy)}
	schema, ok, err := entitySchema(context.Background(), app, "analyst", nil, "orders")
	if err != nil || !ok {
		t.Fatal(err)
	}
	data, _ := json.Marshal(schema)
	text := string(data)
	for _, want := range []string{
		`"actions":["read","aggregate"]`,
		`"read":{"fields":["id"]}`,
		// Counting rows reads no field: each grant's rows count (combining
		// amount and region in one call is refused, with the scopes).
		`"aggregate":{"fields":["id","amount","region"]}`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("schema missing %s: %s", want, text)
		}
	}
	entityFields := schema["fields"].([]map[string]any)
	if len(entityFields) != 3 {
		t.Fatalf("top-level fields must cover every action: %v", entityFields)
	}
}

// Results are structured, and their text is the same JSON: rows wrapped in
// {"result": [...]}, one object as is. Tools describe their behavior.
func TestToolsAreAnnotatedAndResultsStructured(t *testing.T) {
	app := authorizedSchemaTestApp(t)
	tools, err := tool.NewRegistry(tool.DefaultTools())
	if err != nil {
		t.Fatal(err)
	}
	flags := config.DefaultToolFlags()
	flags.UpdateRecord = true
	app.Tools, app.ToolFlags = tools, flags
	session, ctx, cancel := connectMCPSession(t, app)
	defer cancel()
	defer session.Close()

	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*mcp.Tool{}
	for _, listed := range list.Tools {
		byName[listed.Name] = listed
		a := listed.Annotations
		if listed.Title == "" || a == nil || a.Title != listed.Title || a.OpenWorldHint == nil || *a.OpenWorldHint ||
			listed.OutputSchema == nil {
			t.Errorf("%s is not fully described: %+v", listed.Name, listed)
		}
		if !strings.Contains(string(mustJSON(t, listed.InputSchema)), `"additionalProperties":false`) {
			t.Errorf("%s input schema admits unknown properties", listed.Name)
		}
	}
	read, update := byName["read_records"].Annotations, byName["update_record"].Annotations
	if !read.ReadOnlyHint || !read.IdempotentHint || read.DestructiveHint != nil ||
		update.ReadOnlyHint || update.DestructiveHint == nil || !*update.DestructiveHint {
		t.Fatalf("hints: read %+v, update %+v", read, update)
	}

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "describe_entities", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("describe: %v %+v", err, res)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if string(mustJSON(t, res.StructuredContent)) != text || !strings.Contains(text, `"entities":[{`) {
		t.Fatalf("structured %s, text %s", mustJSON(t, res.StructuredContent), text)
	}
	rows := toMCPResult(tool.Result{Content: []map[string]any{{"id": 1}}}, tool.Info{})
	if string(mustJSON(t, rows.StructuredContent)) != `{"result":[{"id":1}]}` ||
		rows.Content[0].(*mcp.TextContent).Text != `{"result":[{"id":1}]}` {
		t.Fatalf("rows result = %+v", rows)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
