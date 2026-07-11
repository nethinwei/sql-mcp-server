// Command protocolsmoke verifies the two supported MCP transports against a
// real database: it runs the server binary over stdio and over streamable
// HTTP, and asserts initialize, tools/list, one allowed read (with masking),
// and one machine-readable denial on each. On HTTP it additionally checks the
// health, readiness, and metrics endpoints. It runs in PR/main CI so protocol
// regressions surface before the release chain.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	pgprov "github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

const (
	httpAddr  = "127.0.0.1:18091"
	httpToken = "protocol-smoke-token"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	dsn, cleanup, err := startDatabase(ctx)
	if err != nil {
		fail("database: %v", err)
	}
	defer cleanup()

	if err := runStdio(ctx, dsn); err != nil {
		fail("stdio: %v", err)
	}
	fmt.Println("protocol smoke: stdio passed")

	if err := runHTTP(ctx, dsn); err != nil {
		fail("http: %v", err)
	}
	fmt.Println("protocol smoke: streamable HTTP passed")
}

func startDatabase(ctx context.Context) (string, func(), error) {
	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("protocolsmoke"),
		postgres.WithUsername("protocolsmoke"),
		postgres.WithPassword("protocolsmoke"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = container.Terminate(context.Background()) }
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	provider, err := pgprov.New(dsn)
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	defer func() { _ = provider.Close() }()
	_, err = provider.ExecContext(ctx, `
		CREATE TABLE app_user (id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY, email text, tenant_id integer);
		INSERT INTO app_user (email, tenant_id) VALUES ('alice@example.com', 7), ('bob@example.com', 8)`)
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	return dsn, cleanup, nil
}

func runStdio(ctx context.Context, dsn string) error {
	command := exec.CommandContext(ctx, binary(), "serve",
		"--config", "internal/protocolsmoke/config.yaml", "--transport", "stdio")
	command.Env = append(os.Environ(), "DATABASE_DSN="+dsn)
	client := mcp.NewClient(&mcp.Implementation{Name: "protocol-smoke", Version: "dev"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = session.Close() }()
	return verifySession(ctx, session)
}

func runHTTP(ctx context.Context, dsn string) error {
	serveCtx, stop := context.WithCancel(ctx)
	defer stop()
	command := exec.CommandContext(serveCtx, binary(), "serve",
		"--config", "internal/protocolsmoke/config.yaml", "--transport", "http", "--addr", httpAddr)
	command.Env = append(os.Environ(), "DATABASE_DSN="+dsn)
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		return fmt.Errorf("start server: %w", err)
	}
	defer func() { stop(); _ = command.Wait() }()

	if err := waitForEndpoint(ctx, "http://"+httpAddr+"/healthz", http.StatusOK, ""); err != nil {
		return fmt.Errorf("liveness: %w", err)
	}
	if err := verifyOperationalEndpoints(ctx); err != nil {
		return err
	}

	session, err := connectHTTP(ctx)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = session.Close() }()
	if err := verifySession(ctx, session); err != nil {
		return err
	}
	return verifyMetrics(ctx)
}

func verifyOperationalEndpoints(ctx context.Context) error {
	if err := waitForEndpoint(ctx, "http://"+httpAddr+"/readyz/snapshot", http.StatusOK, ""); err != nil {
		return fmt.Errorf("snapshot readiness: %w", err)
	}
	if err := waitForEndpoint(ctx, "http://"+httpAddr+"/readyz/db", http.StatusOK, ""); err != nil {
		return fmt.Errorf("database readiness: %w", err)
	}
	return nil
}

// verifyMetrics scrapes /metrics (token-protected) after the session ran at
// least one allowed and one denied call, so the counter must be present.
func verifyMetrics(ctx context.Context) error {
	body, status, err := get(ctx, "http://"+httpAddr+"/metrics", httpToken)
	if err != nil {
		return fmt.Errorf("metrics: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("metrics status = %d", status)
	}
	if !strings.Contains(body, "sql_mcp_tool_calls_total") {
		return fmt.Errorf("metrics output missing tool call counter: %s", body)
	}
	if _, status, _ := get(ctx, "http://"+httpAddr+"/metrics", ""); status != http.StatusUnauthorized {
		return fmt.Errorf("unauthenticated metrics status = %d, want 401", status)
	}
	return nil
}

type headerTransport struct{ base http.RoundTripper }

func (t headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	cloned.Header.Set("Authorization", "Bearer "+httpToken)
	return t.base.RoundTrip(cloned)
}

func connectHTTP(ctx context.Context) (*mcp.ClientSession, error) {
	transport := &mcp.StreamableClientTransport{
		Endpoint:   "http://" + httpAddr + "/mcp",
		HTTPClient: &http.Client{Transport: headerTransport{base: http.DefaultTransport}, Timeout: 15 * time.Second},
		MaxRetries: -1,
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "protocol-smoke", Version: "dev"}, nil)
	return client.Connect(ctx, transport, nil)
}

// verifySession runs the shared assertions: tool discovery, an allowed read
// with masking applied, and a machine-readable denial with a decision ID.
func verifySession(ctx context.Context, session *mcp.ClientSession) error {
	tools, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		return fmt.Errorf("list tools: %w", err)
	}
	visible := map[string]bool{}
	for _, t := range tools.Tools {
		visible[t.Name] = true
	}
	if !visible["read_records"] || visible["delete_record"] {
		return fmt.Errorf("unexpected tool visibility: %v", visible)
	}

	allowed, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "read_records", Arguments: map[string]any{
		"entity": "users",
		"filter": []map[string]any{{"field": "id", "op": "eq", "value": 1}},
	}})
	if err != nil || allowed.IsError {
		return fmt.Errorf("allowed read failed: err=%v result=%s", err, resultText(allowed))
	}
	if text := resultText(allowed); strings.Contains(text, "alice@example.com") || !strings.Contains(text, "a***") {
		return fmt.Errorf("email masking not applied: %s", text)
	}

	denied, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "read_records", Arguments: map[string]any{
		"entity": "users",
	}})
	if err != nil || !denied.IsError {
		return fmt.Errorf("full scan was not rejected: err=%v result=%s", err, resultText(denied))
	}
	raw, err := json.Marshal(denied.StructuredContent)
	if err != nil {
		return fmt.Errorf("marshal denial: %w", err)
	}
	var denial map[string]any
	if err := json.Unmarshal(raw, &denial); err != nil || denial["code"] == "" {
		return fmt.Errorf("denial contract missing: %s", raw)
	}
	if id, _ := denial["decisionId"].(string); id == "" {
		return fmt.Errorf("denial is missing decisionId: %s", raw)
	}
	return nil
}

func waitForEndpoint(ctx context.Context, url string, wantStatus int, token string) error {
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) && ctx.Err() == nil {
		_, status, err := get(ctx, url, token)
		if err == nil && status == wantStatus {
			return nil
		}
		lastErr = fmt.Errorf("status=%d err=%v", status, err)
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("%s not ready: %v", url, lastErr)
}

func get(ctx context.Context, url, token string) (string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", 0, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	return string(body), res.StatusCode, err
}

func resultText(result *mcp.CallToolResult) string {
	if result == nil {
		return "<nil>"
	}
	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Sprintf("<marshal error: %v>", err)
	}
	return string(data)
}

func binary() string {
	if value := os.Getenv("PROTOCOL_BINARY"); value != "" {
		return value
	}
	return "./sql-mcp-server"
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
