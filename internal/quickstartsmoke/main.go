package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultEndpoint = "http://127.0.0.1:8080/mcp"
	defaultToken    = "quickstart-only-token"
)

type headerTransport struct {
	base http.RoundTripper
}

func (t headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	cloned.Header.Set("Authorization", "Bearer "+envOr("MCP_TOKEN", defaultToken))
	cloned.Header.Set("X-MCP-Role", "reader")
	cloned.Header.Set("X-MCP-Subject", `{"tenant_id":7}`)
	return t.base.RoundTrip(cloned)
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	session, err := connect(ctx)
	if err != nil {
		fail("connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	if err := verify(ctx, session); err != nil {
		fail("%v", err)
	}
	fmt.Println("quickstart smoke passed: all six scenarios verified " +
		"(read+mask, field deny, tenant isolation, masked-filter deny, cost deny, structured-error narrowing)")
}

func connect(ctx context.Context) (*mcp.ClientSession, error) {
	httpClient := &http.Client{
		Transport: headerTransport{base: http.DefaultTransport},
		Timeout:   15 * time.Second,
	}
	transport := &mcp.StreamableClientTransport{
		Endpoint:   envOr("MCP_ENDPOINT", defaultEndpoint),
		HTTPClient: httpClient,
		MaxRetries: -1,
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "quickstart-smoke", Version: "v0.1.4"}, nil)

	var lastErr error
	for ctx.Err() == nil {
		session, err := client.Connect(ctx, transport, nil)
		if err == nil {
			return session, nil
		}
		lastErr = err
		time.Sleep(time.Second)
	}
	return nil, fmt.Errorf("%w: %v", ctx.Err(), lastErr)
}

func verify(ctx context.Context, session *mcp.ClientSession) error {
	if err := verifyTools(ctx, session); err != nil {
		return err
	}
	if err := verifyReads(ctx, session); err != nil {
		return err
	}
	if err := verifyMaskedFilterRejected(ctx, session); err != nil {
		return err
	}
	return verifyStructuredErrorNarrowing(ctx, session)
}

func verifyTools(ctx context.Context, session *mcp.ClientSession) error {
	tools, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		return fmt.Errorf("list tools: %w", err)
	}
	visible := make(map[string]bool, len(tools.Tools))
	for _, tool := range tools.Tools {
		visible[tool.Name] = true
	}
	if !visible["read_records"] || visible["delete_record"] {
		return fmt.Errorf(
			"unexpected tool visibility: read=%v delete=%v",
			visible["read_records"],
			visible["delete_record"],
		)
	}
	return nil
}

func verifyReads(ctx context.Context, session *mcp.ClientSession) error {
	allowed, err := callRead(ctx, session, map[string]any{
		"entity": "users",
		"filter": []map[string]any{{"field": "id", "op": "eq", "value": 1}},
	})
	if err != nil || allowed.IsError {
		return fmt.Errorf("authorized read failed: err=%v result=%s", err, resultText(allowed))
	}
	text := resultText(allowed)
	if strings.Contains(text, "alice@example.com") || !strings.Contains(text, "a***") {
		return fmt.Errorf("email masking not applied: %s", text)
	}

	isolated, err := callRead(ctx, session, map[string]any{
		"entity": "users",
		"filter": []map[string]any{{"field": "id", "op": "eq", "value": 2}},
	})
	if err != nil || isolated.IsError || strings.Contains(resultText(isolated), "bob") {
		return fmt.Errorf("tenant isolation failed: err=%v result=%s", err, resultText(isolated))
	}

	fullScan, err := callRead(ctx, session, map[string]any{"entity": "users"})
	if err != nil || !fullScan.IsError {
		return fmt.Errorf("full scan was not rejected: err=%v result=%s", err, resultText(fullScan))
	}

	hiddenField, err := callRead(ctx, session, map[string]any{
		"entity": "users",
		"fields": []string{"tenant_id"},
		"filter": []map[string]any{{"field": "id", "op": "eq", "value": 1}},
	})
	if err != nil || !hiddenField.IsError {
		return fmt.Errorf("hidden field was not rejected: err=%v result=%s", err, resultText(hiddenField))
	}
	return nil
}

// verifyMaskedFilterRejected proves the masked email can be returned (already
// checked in verifyReads) but not used as a filter, which would allow
// equality probing of the masked value.
func verifyMaskedFilterRejected(ctx context.Context, session *mcp.ClientSession) error {
	res, err := callRead(ctx, session, map[string]any{
		"entity": "users",
		"filter": []map[string]any{{"field": "email", "op": "eq", "value": "alice@example.com"}},
	})
	if err != nil || !res.IsError {
		return fmt.Errorf("masked-field filter was not rejected: err=%v result=%s", err, resultText(res))
	}
	return nil
}

// verifyStructuredErrorNarrowing proves the agent self-repair loop: a cost
// rejection carries the machine-readable denial contract, and a request
// narrowed per its hints succeeds.
func verifyStructuredErrorNarrowing(ctx context.Context, session *mcp.ClientSession) error {
	rejected, err := callRead(ctx, session, map[string]any{"entity": "users"})
	if err != nil || !rejected.IsError {
		return fmt.Errorf("full scan was not rejected: err=%v result=%s", err, resultText(rejected))
	}
	denial, err := denialContract(rejected)
	if err != nil {
		return err
	}
	if denial["code"] != "COST_EXCEEDED" || denial["retryable"] != true {
		return fmt.Errorf("unexpected denial contract: %v", denial)
	}
	if id, _ := denial["decisionId"].(string); id == "" {
		return fmt.Errorf("denial is missing decisionId: %v", denial)
	}
	if hints, _ := denial["hints"].([]any); len(hints) == 0 {
		return fmt.Errorf("denial is missing rewrite hints: %v", denial)
	}
	narrowed, err := callRead(ctx, session, map[string]any{
		"entity": "users",
		"filter": []map[string]any{{"field": "id", "op": "eq", "value": 1}},
		"limit":  1,
	})
	if err != nil || narrowed.IsError {
		return fmt.Errorf("narrowed retry failed: err=%v result=%s", err, resultText(narrowed))
	}
	return nil
}

func denialContract(res *mcp.CallToolResult) (map[string]any, error) {
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		return nil, fmt.Errorf("marshal structuredContent: %w", err)
	}
	var denial map[string]any
	if err := json.Unmarshal(raw, &denial); err != nil || len(denial) == 0 {
		return nil, fmt.Errorf("denial structuredContent missing: %s err=%v", raw, err)
	}
	return denial, nil
}

func callRead(ctx context.Context, session *mcp.ClientSession, args map[string]any) (*mcp.CallToolResult, error) {
	return session.CallTool(ctx, &mcp.CallToolParams{Name: "read_records", Arguments: args})
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

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
