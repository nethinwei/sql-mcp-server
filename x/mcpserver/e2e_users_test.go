//go:build e2e

package mcpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/mcpserver"
)

type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	cloned.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(cloned)
}

// e2eUsersConfig gives alice two roles that differ in both fields and rows:
// ids reads id/tenant_id on every row, emails reads id/email on tenant 7 only.
func e2eUsersConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := e2eSubjectScopedConfig("")
	cfg.Entities[0].Roles = config.RoleConfig{}
	cfg.Entities[0].FieldACL = nil
	cfg.Entities[0].RowPolicies = nil
	cfg.Roles = map[string]config.RoleDefinition{
		"ids": {Grants: []config.GrantConfig{{
			Entity: "users", Actions: []string{"read"},
			Fields: &config.FieldACLConfig{Read: []string{"id", "tenant_id"}},
		}}},
		"emails": {Grants: []config.GrantConfig{{
			Entity: "users", Actions: []string{"read"},
			Fields: &config.FieldACLConfig{Read: []string{"id", "email"}},
			Rows:   config.FilterConfig{"op": "eq", "field": "tenant_id", "value": 7},
		}}},
	}
	cfg.Users = map[string]config.UserConfig{
		"alice": {TokenHash: config.TokenHash("alice-token"), Roles: []string{"ids", "emails"}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func connectAsUser(t *testing.T, server *httptest.Server, token string) (*mcp.ClientSession, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	httpClient := &http.Client{Transport: bearerTransport{base: http.DefaultTransport, token: token}}
	t.Cleanup(httpClient.CloseIdleConnections)
	transport := &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: httpClient}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "e2e-users"}, nil).Connect(ctx, transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, ctx
}

func readUser(t *testing.T, ctx context.Context, s *mcp.ClientSession, id int, fields ...string) *mcp.CallToolResult {
	t.Helper()
	args := map[string]any{
		"entity": "users",
		"filter": []map[string]any{{"field": "id", "op": "eq", "value": id}},
	}
	if len(fields) > 0 {
		args["fields"] = fields
	}
	res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: "read_records", Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// TestE2EHTTPUserMultiRoleCoverage proves over real HTTP and PostgreSQL that a
// user token selects the user's grants, and that merging two roles never
// exposes a field on rows only another role may see.
func TestE2EHTTPUserMultiRoleCoverage(t *testing.T) {
	prov, cleanup := startE2EPostgres(t)
	defer cleanup()
	app, err := bootstrap.AssembleWithProvider(e2eUsersConfig(t), prov)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	handler, err := mcpserver.Handler(mcpserver.NewServer(app), mcpserver.HTTPConfig{
		Addr: "127.0.0.1:0", Users: app,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	resp, err := http.Post(server.URL+"/mcp", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("configured users must require a token: status %d", resp.StatusCode)
	}

	alice, ctx := connectAsUser(t, server, "alice-token")
	if res := readUser(t, ctx, alice, 1, "id", "email"); res.IsError || !contentContains(res, "a***@x.com") {
		t.Fatalf("tenant 7 email must be visible through the emails role: %+v", res.Content)
	}
	if res := readUser(t, ctx, alice, 2, "id", "email"); res.IsError || contentContains(res, "b***@x.com") {
		t.Fatalf("tenant 8 email must stay hidden: %+v", res.Content)
	}
	if res := readUser(t, ctx, alice, 2, "id", "tenant_id"); res.IsError || !contentContains(res, "8") {
		t.Fatalf("tenant_id must be visible on every row through the ids role: %+v", res.Content)
	}
	res := readUser(t, ctx, alice, 1)
	structured, _ := json.Marshal(res.StructuredContent)
	if !res.IsError || !strings.Contains(string(structured), `"code":"AMBIGUOUS_FIELD_SCOPE"`) ||
		!strings.Contains(string(structured), `"fieldScopes"`) {
		t.Fatalf("default projection across both roles must be ambiguous: %s", structured)
	}
}
