package mcpserver

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nethinwei/sql-mcp-server/entity"
	"github.com/nethinwei/sql-mcp-server/tool"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
)

func TestIsLoopbackAddr(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8080", true},
		{"localhost:8080", true},
		{"[::1]:8080", true},
		{":8080", false},
		{"0.0.0.0:8080", false},
		{"[::]:8080", false},
		{"192.168.1.10:8080", false},
	}
	for _, c := range cases {
		if got := isLoopbackAddr(c.addr); got != c.want {
			t.Errorf("isLoopbackAddr(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

func TestValidateHTTPSecurity(t *testing.T) {
	cases := []struct {
		name    string
		cfg     HTTPConfig
		wantErr bool
	}{
		{"loopback no auth ok", HTTPConfig{Addr: "127.0.0.1:8080"}, false},
		{"exposed no auth refused", HTTPConfig{Addr: ":8080"}, true},
		{"exposed with token ok", HTTPConfig{Addr: ":8080", Token: "s3cret"}, false},
		{"exposed with mtls needs cert", HTTPConfig{Addr: ":8080", ClientCA: "ca.pem"}, true},
		{"exposed with mtls and cert ok", HTTPConfig{Addr: ":8080", ClientCA: "ca.pem", TLSCert: "c.pem", TLSKey: "k.pem"}, false},
		{"cert without key refused", HTTPConfig{Addr: "127.0.0.1:8080", TLSCert: "c.pem"}, true},
		{"key without cert refused", HTTPConfig{Addr: "127.0.0.1:8080", TLSKey: "k.pem"}, true},
		{"proxy headers without boundary refused", HTTPConfig{Addr: "127.0.0.1:8080", TrustProxyHeaders: true}, true},
		{"proxy headers with CIDR ok", HTTPConfig{Addr: "127.0.0.1:8080", TrustProxyHeaders: true, TrustedProxyCIDRs: []string{"127.0.0.0/8"}}, false},
	}
	for _, c := range cases {
		err := validateHTTPSecurity(c.cfg)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: validateHTTPSecurity err=%v, wantErr=%v", c.name, err, c.wantErr)
		}
	}
}

func TestTokenAuth(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := tokenAuth("s3cret", next)
	cases := []struct {
		name       string
		authHeader string
		wantCode   int
	}{
		{"valid", "Bearer s3cret", http.StatusOK},
		{"case-insensitive scheme", "bearer s3cret", http.StatusOK},
		{"missing", "", http.StatusUnauthorized},
		{"wrong", "Bearer nope", http.StatusUnauthorized},
		{"no scheme", "s3cret", http.StatusUnauthorized},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		if c.authHeader != "" {
			req.Header.Set("Authorization", c.authHeader)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.wantCode {
			t.Errorf("%s: code = %d, want %d", c.name, rec.Code, c.wantCode)
		}
	}
}

func TestLimitBodyRejectsOversized(t *testing.T) {
	var readErr error
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	})
	h := limitBody(8, next)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("0123456789")) // 10 bytes > 8
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if readErr == nil {
		t.Fatal("expected read error for oversized body, got nil")
	}
}

func TestSessionEventStoreReportsDisconnect(t *testing.T) {
	var closed string
	identities := newSessionIdentityStore()
	identities.bind("session-1", sessionIdentity{role: "reader"})
	store := &sessionEventStore{
		EventStore: mcp.NewMemoryEventStore(nil),
		onClosed:   func(session string) { closed = session },
		identity:   identities,
	}
	if err := store.SessionClosed(context.Background(), "session-1"); err != nil {
		t.Fatal(err)
	}
	if closed != "session-1" {
		t.Fatalf("closed session = %q", closed)
	}
	if identities.matches("session-1", sessionIdentity{role: "reader"}) {
		t.Fatal("closed session identity was not removed")
	}
}

func TestBindSessionIdentityNormalizesAndEnforcesIdentity(t *testing.T) {
	store := newSessionIdentityStore()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(sessionIDHeader) == "" {
			w.Header().Set(sessionIDHeader, "session-1")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	handler := withRequestSubject(bindSessionIdentity(store, next))

	create := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	create.Header.Set("X-MCP-Role", " reader ")
	create.Header.Set("X-MCP-Subject", `{"tenant":1,"region":"eu"}`)
	handler.ServeHTTP(httptest.NewRecorder(), create)

	for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
		req := httptest.NewRequest(method, "/mcp", nil)
		req.Header.Set(sessionIDHeader, "session-1")
		req.Header.Set("X-MCP-Role", "reader")
		req.Header.Set("X-MCP-Subject", `{"region":"eu","tenant":1}`)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("%s with normalized identity code = %d", method, rec.Code)
		}
	}

	mismatch := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	mismatch.Header.Set(sessionIDHeader, "session-1")
	mismatch.Header.Set("X-MCP-Role", "admin")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, mismatch)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("identity mismatch code = %d, want 403", rec.Code)
	}
}

func TestBindSessionIdentityConcurrentAccess(t *testing.T) {
	store := newSessionIdentityStore()
	identity := sessionIdentity{role: "reader", subject: `{"tenant":1}`}
	if !store.bind("session-1", identity) {
		t.Fatal("initial bind failed")
	}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !store.matches("session-1", identity) {
				t.Error("concurrent identity lookup failed")
			}
		}()
	}
	wg.Wait()
}

func TestWithRequestSubjectOnlyWhenTrusted(t *testing.T) {
	// When TrustProxyHeaders is false, ServeHTTP must not wrap withRequestSubject,
	// so a forged header cannot set the subject. We assert the middleware itself
	// reads headers (trusted path) and that subjectFromContext falls back to the
	// default role when no subject is present (untrusted path).
	var gotRole string
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		role, _ := subjectFromContext(r.Context(), "default")
		gotRole = role
	})

	// Trusted: header is honored.
	trusted := withRequestSubject(next)
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("X-MCP-Role", "admin")
	trusted.ServeHTTP(httptest.NewRecorder(), req)
	if gotRole != "admin" {
		t.Fatalf("trusted path: role = %q, want admin", gotRole)
	}

	// Untrusted: same header, but not wrapped -> default role.
	req2 := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req2.Header.Set("X-MCP-Role", "admin")
	next.ServeHTTP(httptest.NewRecorder(), req2)
	if gotRole != "default" {
		t.Fatalf("untrusted path: role = %q, want default", gotRole)
	}
}

func TestWithRequestSubjectRejectsMalformedSubject(t *testing.T) {
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("malformed subject must not reach handler")
	})
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("X-MCP-Subject", `{bad`)
	rec := httptest.NewRecorder()
	withRequestSubject(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}

func TestWithRequestSubjectPreservesLargeInteger(t *testing.T) {
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, subject := subjectFromContext(r.Context(), "reader")
		if got := subject["tenant"]; got == nil || got.(interface{ String() string }).String() != "9007199254740993" {
			t.Fatalf("tenant = %#v", got)
		}
	})
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("X-MCP-Subject", `{"tenant":9007199254740993}`)
	withRequestSubject(next).ServeHTTP(httptest.NewRecorder(), req)
}

func TestTrustedProxyOnly(t *testing.T) {
	networks, err := parseTrustedProxyCIDRs([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := trustedProxyOnly(networks, next)
	trusted := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	trusted.RemoteAddr = "10.2.3.4:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, trusted)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("trusted code = %d", rec.Code)
	}
	untrusted := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	untrusted.RemoteAddr = "192.0.2.1:1234"
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, untrusted)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("untrusted code = %d, want 403", rec.Code)
	}
}

func TestNewServerRegistersProcedureCustomTools(t *testing.T) {
	e := entity.Entity{
		Name:   "refresh-cache",
		Kind:   entity.KindProcedure,
		Params: []string{"tenant"},
		MCP:    entity.MCPFlags{CustomTool: true, TrustedProcedure: true},
	}
	entities, err := entity.NewRegistry([]entity.Entity{e})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := tool.NewRegistry(tool.DefaultTools())
	if err != nil {
		t.Fatal(err)
	}
	app := &bootstrap.App{Registry: entities, Tools: tools}
	srv := NewServer(app)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Run(ctx, serverTransport) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	list, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	want := tool.ProcedureToolName(e.Name)
	for _, registered := range list.Tools {
		if registered.Name == want {
			return
		}
	}
	t.Fatalf("custom procedure tool %q not registered", want)
}
