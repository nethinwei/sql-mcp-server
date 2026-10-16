package mcpserver_test

import (
	"context"
	"errors"
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

// A session that expired is gone the way the protocol says: the client gets
// "session not found" (404), not a refusal, and a new session works.
func TestExpiredSessionIsNotFoundAndANewOneWorks(t *testing.T) {
	t.Parallel()
	cfg := toolsConfig(false)
	cfg.Users = map[string]config.UserConfig{"alice": {TokenHash: config.TokenHash("alice-token")}}
	cfg.ApplyDefaults()
	app, err := bootstrap.AssembleWithProvider(cfg, &instantProvider{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	handler, err := mcpserver.Handler(mcpserver.NewServer(app), mcpserver.HTTPConfig{
		Addr: "127.0.0.1:0", HTTPAuth: mcpserver.HTTPAuth{Users: app}, SessionTimeout: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	session, ctx := connectWithToken(t, server, "alice-token")
	if _, err := session.ListTools(ctx, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(600 * time.Millisecond) // idle past the timeout
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer alice-token")
	req.Header.Set("Mcp-Session-Id", session.ID())
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("the expired session = %d, want 404", resp.StatusCode)
	}
	if _, err := session.ListTools(ctx, nil); !errors.Is(err, mcp.ErrSessionMissing) {
		t.Fatalf("the client on the expired session got %v, want session not found", err)
	}
	again, ctx := connectWithToken(t, server, "alice-token")
	if _, err := again.ListTools(context.WithoutCancel(ctx), nil); err != nil {
		t.Fatalf("a new session: %v", err)
	}
}

// Revoking a user closes their open sessions in the transport too, not only
// their bindings, so nothing is held for them until the idle timeout.
func TestRevokedSessionIsClosed(t *testing.T) {
	t.Parallel()
	cfg := toolsConfig(false)
	cfg.Users = map[string]config.UserConfig{"alice": {TokenHash: config.TokenHash("alice-token")}}
	cfg.ApplyDefaults()
	app, err := bootstrap.AssembleWithProvider(cfg, &instantProvider{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	var revoke func([]string) []string
	mcpServer := mcpserver.NewServer(app)
	handler, err := mcpserver.Handler(mcpServer, mcpserver.HTTPConfig{
		Addr: "127.0.0.1:0", HTTPAuth: mcpserver.HTTPAuth{Users: app},
		RevokedPrincipals: func(f func([]string) []string) { revoke = f },
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	session, ctx := connectWithToken(t, server, "alice-token")
	if _, err := session.ListTools(ctx, nil); err != nil {
		t.Fatal(err)
	}
	open := func() int {
		n := 0
		for range mcpServer.Sessions() {
			n++
		}
		return n
	}
	if open() != 1 {
		t.Fatalf("open sessions = %d, want 1", open())
	}
	if got := revoke([]string{"user:alice"}); len(got) != 1 || got[0] != session.ID() {
		t.Fatalf("revoked %v", got)
	}
	if open() != 0 {
		t.Fatalf("open sessions after revoking = %d, want 0", open())
	}
}

// A cross-origin browser request is refused before it reaches a session.
func TestCrossOriginRequestsAreRefused(t *testing.T) {
	t.Parallel()
	cfg := toolsConfig(false)
	cfg.Users = map[string]config.UserConfig{"alice": {TokenHash: config.TokenHash("alice-token")}}
	cfg.ApplyDefaults()
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
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer alice-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Origin", "https://attacker.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin request = %d, want 403", resp.StatusCode)
	}
	// A client that is not a browser sends no Origin and still connects.
	session, ctx := connectWithToken(t, server, "alice-token")
	if _, err := session.ListTools(ctx, nil); err != nil {
		t.Fatal(err)
	}
}
