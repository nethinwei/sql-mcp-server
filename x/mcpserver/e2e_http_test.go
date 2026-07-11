//go:build e2e

package mcpserver_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/mcpserver"
)

const e2eHTTPToken = "e2e-http-token"

// identityTransport injects the bearer token and proxy identity headers that a
// production gateway would set.
type identityTransport struct {
	base    http.RoundTripper
	role    string
	subject string
}

func (t identityTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	cloned.Header.Set("Authorization", "Bearer "+e2eHTTPToken)
	if t.role != "" {
		cloned.Header.Set("X-MCP-Role", t.role)
	}
	if t.subject != "" {
		cloned.Header.Set("X-MCP-Subject", t.subject)
	}
	return t.base.RoundTrip(cloned)
}

func startE2EHTTPServer(t *testing.T, app *bootstrap.App) *httptest.Server {
	t.Helper()
	handler, err := mcpserver.Handler(mcpserver.NewServer(app), mcpserver.HTTPConfig{
		Addr:              "127.0.0.1:0",
		Token:             e2eHTTPToken,
		TrustProxyHeaders: true,
		TrustedProxyCIDRs: []string{"127.0.0.0/8", "::1/128"},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func connectE2EHTTPSession(
	t *testing.T,
	server *httptest.Server,
	role, subject string,
) (*mcp.ClientSession, context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	httpClient := &http.Client{
		Transport: identityTransport{base: http.DefaultTransport, role: role, subject: subject},
	}
	t.Cleanup(httpClient.CloseIdleConnections)
	transport := &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: httpClient}
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e-http-test"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	return session, ctx, cancel
}

// TestE2EHTTPAuthorizationParity runs the exact assertion suite of the
// in-memory e2e over a real streamable HTTP listener with bearer auth and
// proxy identity headers, proving the key authorization paths are equivalent
// across transports.
func TestE2EHTTPAuthorizationParity(t *testing.T) {
	app, cleanup := setupApp(t)
	defer cleanup()
	server := startE2EHTTPServer(t, app)

	assertE2EHTTPAuthBoundary(t, server)

	session, ctx, cancel := connectE2EHTTPSession(t, server, "operator", "")
	defer cancel()
	defer session.Close()

	assertE2EToolVisibility(t, ctx, session)
	assertE2EPKReadAndMasking(t, ctx, session)
	assertE2ERLSAndRBAC(t, ctx, session)
	assertE2EUnsafeWriteRejected(t, ctx, session)
	assertE2ETransactionLifecycle(t, ctx, session)
	assertE2EFullScanRejected(t, ctx, session)
	assertE2ESchemaResourceAndPrompts(t, ctx, session)
}

func assertE2EHTTPAuthBoundary(t *testing.T, server *httptest.Server) {
	t.Helper()
	resp, err := http.Post(server.URL+"/mcp", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /mcp status = %d, want 401", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer wrong-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong-token /mcp status = %d, want 401", resp.StatusCode)
	}
	resp, err = http.Get(server.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz status = %d, want 200", resp.StatusCode)
	}
}

func e2eSubjectScopedConfig(auditPath string) *config.Config {
	cfg := &config.Config{
		Server:   config.ServerConfig{Role: "denied"},
		Database: config.DatabaseConfig{Driver: "postgres", DSN: "ignored"},
		Entities: []config.EntityConfig{{
			Name: "users", Source: "users", Kind: "table", PrimaryKey: []string{"id"},
			Fields: []config.FieldConfig{{Name: "id"}, {Name: "email", Mask: "email"}, {Name: "tenant_id"}},
			Roles:  config.RoleConfig{Read: []string{"reader"}},
			FieldACL: map[string]config.FieldACLConfig{
				"reader": {Read: []string{"id", "email"}},
			},
			RowPolicies: config.RowPolicies{
				"reader": config.FilterConfig{
					"op": "eq", "field": "tenant_id", "value": "${subject.tenant_id}",
				},
			},
		}},
		Tools: config.DefaultToolFlags(),
		Cost: config.CostConfig{
			Enabled: config.Bool(true), SoftScore: 60, HardScore: 40, MaxRows: 10000,
			RejectFullScan: true, WhitelistPKPoint: true,
		},
	}
	if auditPath != "" {
		cfg.Audit = config.AuditConfig{Enabled: true, Path: auditPath}
	}
	cfg.ApplyDefaults()
	return cfg
}

// TestE2EHTTPSubjectTenantScoping proves the X-MCP-Subject header drives the
// row policy: the same PK read returns the row for the owning tenant and an
// empty result for another tenant.
func TestE2EHTTPSubjectTenantScoping(t *testing.T) {
	prov, cleanup := startE2EPostgres(t)
	defer cleanup()
	app, err := bootstrap.AssembleWithProvider(e2eSubjectScopedConfig(""), prov)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	server := startE2EHTTPServer(t, app)

	owner, ctx, cancel := connectE2EHTTPSession(t, server, "reader", `{"tenant_id":7}`)
	defer cancel()
	defer owner.Close()
	res, err := owner.CallTool(ctx, &mcp.CallToolParams{
		Name: "read_records",
		Arguments: map[string]any{
			"entity": "users",
			"filter": []map[string]any{{"field": "id", "op": "eq", "value": 1}},
		},
	})
	if err != nil || res.IsError {
		t.Fatalf("owning tenant read failed: result=%+v error=%v", res, err)
	}
	if !contentContains(res, "a***@x.com") {
		t.Fatalf("owning tenant should see masked row: %+v", res.Content)
	}

	other, ctx2, cancel2 := connectE2EHTTPSession(t, server, "reader", `{"tenant_id":8}`)
	defer cancel2()
	defer other.Close()
	res, err = other.CallTool(ctx2, &mcp.CallToolParams{
		Name: "read_records",
		Arguments: map[string]any{
			"entity": "users",
			"filter": []map[string]any{{"field": "id", "op": "eq", "value": 1}},
		},
	})
	if err != nil || res.IsError {
		t.Fatalf("cross-tenant read failed: result=%+v error=%v", res, err)
	}
	var rows []map[string]any
	decodeTextContent(t, res, &rows)
	if len(rows) != 0 {
		t.Fatalf("cross-tenant read leaked rows: %+v", rows)
	}
}

// TestE2EHTTPDenialAuditCorrelation proves the decision ID returned in the
// machine-readable denial matches the audit event written for the same call.
func TestE2EHTTPDenialAuditCorrelation(t *testing.T) {
	prov, cleanup := startE2EPostgres(t)
	defer cleanup()
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	app, err := bootstrap.AssembleWithProvider(e2eSubjectScopedConfig(auditPath), prov)
	if err != nil {
		t.Fatal(err)
	}
	server := startE2EHTTPServer(t, app)

	session, ctx, cancel := connectE2EHTTPSession(t, server, "reader", `{"tenant_id":7}`)
	defer cancel()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "read_records",
		Arguments: map[string]any{
			"entity": "users",
			"fields": []string{"tenant_id"},
			"filter": []map[string]any{{"field": "id", "op": "eq", "value": 1}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	denial := denialFields(t, res)
	if denial["code"] != "UNAUTHORIZED" {
		t.Fatalf("denial code = %v", denial["code"])
	}
	decisionID, _ := denial["decisionId"].(string)
	if decisionID == "" {
		t.Fatalf("denial missing decision ID: %v", denial)
	}
	_ = session.Close()
	_ = app.Close() // drains the async audit queue

	if !auditContainsDecision(t, auditPath, decisionID) {
		t.Fatalf("audit log has no event with DecisionID %q", decisionID)
	}
}

func denialFields(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	if !res.IsError {
		t.Fatalf("expected denial, got success: %+v", res)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields) == 0 {
		t.Fatalf("denial StructuredContent missing: %s err=%v", raw, err)
	}
	return fields
}

func auditContainsDecision(t *testing.T, path, decisionID string) bool {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event struct {
			DecisionID string `json:"decisionId"` // frozen audit schema field name
		}
		if json.Unmarshal(scanner.Bytes(), &event) == nil && event.DecisionID == decisionID {
			return true
		}
	}
	return false
}
