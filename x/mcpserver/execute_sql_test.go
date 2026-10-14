package mcpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/mcpserver"
)

// escapeConfig admits execute_sql on the default datasource for alice (role
// dba) and not for bob; on switches the tool, permitted grants alice the
// permission.
func escapeConfig(t *testing.T, on, permitted bool) *config.Config {
	t.Helper()
	cfg := toolsConfig(false)
	cfg.Database.SQLEscape = config.SQLEscapeConfig{Connection: config.DefaultConnection}
	cfg.Databases = nil
	cfg.Roles = map[string]config.RoleDefinition{"dba": {}}
	if permitted {
		cfg.Roles["dba"] = config.RoleDefinition{Permissions: []string{"sql:execute@default"}}
	}
	cfg.Users = map[string]config.UserConfig{
		"alice": {TokenHash: config.TokenHash("alice-token"), Roles: []string{"dba"}},
		"bob":   {TokenHash: config.TokenHash("bob-token")},
	}
	cfg.Audit = config.AuditConfig{Enabled: true, Path: t.TempDir() + "/audit.jsonl"}
	cfg.Tools.ExecuteSQL = on
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

type tokenTransport struct{ token string }

func (t tokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(req)
}

func connectWithToken(t *testing.T, server *httptest.Server, token string) (*mcp.ClientSession, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	client := &http.Client{Transport: tokenTransport{token: token}}
	t.Cleanup(client.CloseIdleConnections)
	session, err := mcp.NewClient(&mcp.Implementation{Name: "escape-test"}, nil).
		Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: client}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, ctx
}

func callExecuteSQL(t *testing.T, ctx context.Context, s *mcp.ClientSession) *mcp.CallToolResult {
	t.Helper()
	res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: "execute_sql", Arguments: map[string]any{
		"datasource": "default", "sql": "select id, email from users",
	}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// Over HTTP, execute_sql is listed to the user holding sql:execute on an
// admitting datasource and hidden from the others; a hidden tool is also
// refused when called, and the switch off hides it from everyone.
func TestExecuteSQLListedAndCalledPerUser(t *testing.T) {
	t.Parallel()
	serve := func(cfg *config.Config) *httptest.Server {
		app, err := bootstrap.AssembleWithProvider(cfg, &instantProvider{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = app.Close() })
		handler, err := mcpserver.Handler(mcpserver.NewServer(app), mcpserver.HTTPConfig{
			Addr: "127.0.0.1:0", HTTPAuth: mcpserver.HTTPAuth{Users: app},
		})
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		return server
	}
	server := serve(escapeConfig(t, true, true))
	alice, ctx := connectWithToken(t, server, "alice-token")
	if !slices.Contains(listedToolNames(ctx, t, alice), "execute_sql") {
		t.Fatal("alice must see execute_sql")
	}
	res := callExecuteSQL(t, ctx, alice)
	if res.IsError || !resultHas(res, `"columns":["id","email"]`) || !resultHas(res, "alice@x.com") {
		t.Fatalf("alice's call = %+v", res.Content)
	}
	bob, ctx := connectWithToken(t, server, "bob-token")
	if slices.Contains(listedToolNames(ctx, t, bob), "execute_sql") {
		t.Fatal("bob must not see execute_sql")
	}
	if res := callExecuteSQL(t, ctx, bob); !res.IsError || !structuredCode(res, "UNAUTHORIZED") {
		t.Fatalf("bob's call = %+v", res)
	}
	for name, cfg := range map[string]*config.Config{
		"switch off":    escapeConfig(t, false, true),
		"not permitted": escapeConfig(t, true, false),
	} {
		server := serve(cfg)
		alice, ctx := connectWithToken(t, server, "alice-token")
		if slices.Contains(listedToolNames(ctx, t, alice), "execute_sql") {
			t.Fatalf("%s: execute_sql listed", name)
		}
		if res, err := alice.CallTool(ctx, &mcp.CallToolParams{Name: "execute_sql", Arguments: map[string]any{
			"datasource": "default", "sql": "select 1",
		}}); err == nil && (!res.IsError || !structuredCode(res, "UNAUTHORIZED")) {
			t.Fatalf("%s: call = %+v", name, res)
		}
	}
}

// Behind a trusted proxy, X-MCP-Role naming alice's principal neither
// connects nor reaches execute_sql; X-MCP-User names her (TM-010).
func TestExecuteSQLRoleHeaderCannotImpersonateUser(t *testing.T) {
	t.Parallel()
	app, err := bootstrap.AssembleWithProvider(escapeConfig(t, true, true), &instantProvider{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	handler, err := mcpserver.Handler(mcpserver.NewServer(app), mcpserver.HTTPConfig{
		Addr: "127.0.0.1:0", HTTPAuth: mcpserver.HTTPAuth{
			Users: app, TrustProxyHeaders: true, TrustedProxyCIDRs: []string{"127.0.0.1/32"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	connect := func(header, value string) (*mcp.ClientSession, context.Context, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		t.Cleanup(cancel)
		client := &http.Client{Transport: headerTransport{header: header, value: value}}
		t.Cleanup(client.CloseIdleConnections)
		session, err := mcp.NewClient(&mcp.Implementation{Name: "escape-test"}, nil).
			Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: client}, nil)
		if err == nil {
			t.Cleanup(func() { _ = session.Close() })
		}
		return session, ctx, err
	}
	if _, _, err := connect("X-MCP-Role", "user:alice"); err == nil {
		t.Fatal("X-MCP-Role: user:alice must be refused")
	}
	alice, ctx, err := connect("X-MCP-User", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if res := callExecuteSQL(t, ctx, alice); res.IsError {
		t.Fatalf("X-MCP-User alice: %+v", res.Content)
	}
}

type headerTransport struct{ header, value string }

func (t headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set(t.header, t.value)
	return http.DefaultTransport.RoundTrip(req)
}

// On stdio the process's default identity decides: a configured
// server.user holding the permission sees and uses the tool, the role
// identity never does.
func TestExecuteSQLStdioDefaultIdentity(t *testing.T) {
	t.Parallel()
	for name, user := range map[string]string{"server.user alice": "alice", "server.role only": ""} {
		cfg := escapeConfig(t, true, true)
		cfg.Server.User = user
		app, err := bootstrap.AssembleWithProvider(cfg, &instantProvider{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = app.Close() })
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		serverT, clientT := mcp.NewInMemoryTransports()
		go func() { _ = mcpserver.NewServer(app).Run(ctx, serverT) }()
		session, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, clientT, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = session.Close() })
		listed := slices.Contains(listedToolNames(ctx, t, session), "execute_sql")
		res := callExecuteSQL(t, ctx, session)
		if want := user != ""; listed != want || res.IsError == want {
			t.Fatalf("%s: listed %v, call error %v", name, listed, res.IsError)
		}
	}
}

// A reload revoking the permission refuses the user's next call at once and
// hides the tool at the next tools/list.
func TestReloadRevokesExecuteSQL(t *testing.T) {
	t.Parallel()
	assemble := func(permitted bool) *bootstrap.App {
		cfg := escapeConfig(t, true, permitted)
		cfg.Server.User = "alice"
		app, err := bootstrap.AssembleWithProvider(cfg, &instantProvider{})
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
	serverT, clientT := mcp.NewInMemoryTransports()
	go func() { _ = mcpserver.NewRuntimeServer(runtime).Run(ctx, serverT) }()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if !slices.Contains(listedToolNames(ctx, t, session), "execute_sql") || callExecuteSQL(t, ctx, session).IsError {
		t.Fatal("alice must use execute_sql before the reload")
	}
	if err := runtime.Reload("ignored"); err != nil {
		t.Fatal(err)
	}
	if res := callExecuteSQL(t, ctx, session); !res.IsError || !structuredCode(res, "UNAUTHORIZED") {
		t.Fatalf("after the reload: %+v", res)
	}
	if slices.Contains(listedToolNames(ctx, t, session), "execute_sql") {
		t.Fatal("execute_sql still listed after the permission was revoked")
	}
}

func structuredCode(res *mcp.CallToolResult, code string) bool {
	structured, _ := json.Marshal(res.StructuredContent)
	return strings.Contains(string(structured), `"code":"`+code+`"`)
}

func resultHas(res *mcp.CallToolResult, want string) bool {
	for _, c := range res.Content {
		if text, ok := c.(*mcp.TextContent); ok && strings.Contains(text.Text, want) {
			return true
		}
	}
	return false
}
