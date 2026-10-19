package mcpserver

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/tool"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
)

// strongToken is a shared token long enough for a listener beyond loopback.
const strongToken = "0123456789abcdef0123456789abcdef"

func TestValidateHTTPSecurity(t *testing.T) {
	cases := []struct {
		name    string
		cfg     HTTPConfig
		wantErr bool
	}{
		{"loopback no auth ok", HTTPConfig{Addr: "127.0.0.1:8080"}, false},
		{"exposed no auth refused", HTTPConfig{Addr: ":8080"}, true},
		{"exposed with a strong token ok", HTTPConfig{Addr: ":8080", HTTPAuth: HTTPAuth{Token: strongToken}}, false},
		{"exposed with a short token refused", HTTPConfig{Addr: ":8080", HTTPAuth: HTTPAuth{Token: "s3cret"}}, true},
		{"loopback with a short token ok", HTTPConfig{Addr: "127.0.0.1:8080", HTTPAuth: HTTPAuth{Token: "s3cret"}}, false},
		{"exposed with mtls needs cert", HTTPConfig{Addr: ":8080", HTTPAuth: HTTPAuth{ClientCA: "ca.pem"}}, true},
		{
			"exposed with mtls and cert ok",
			HTTPConfig{Addr: ":8080", HTTPAuth: HTTPAuth{ClientCA: "ca.pem", TLSCert: "c.pem", TLSKey: "k.pem"}},
			false,
		},
		{"cert without key refused", HTTPConfig{Addr: "127.0.0.1:8080", HTTPAuth: HTTPAuth{TLSCert: "c.pem"}}, true},
		{"key without cert refused", HTTPConfig{Addr: "127.0.0.1:8080", HTTPAuth: HTTPAuth{TLSKey: "k.pem"}}, true},
		{"proxy headers without boundary refused",
			HTTPConfig{Addr: "127.0.0.1:8080", HTTPAuth: HTTPAuth{TrustProxyHeaders: true}}, true},
		{
			"proxy headers with CIDR ok",
			HTTPConfig{Addr: "127.0.0.1:8080",
				HTTPAuth: HTTPAuth{TrustProxyHeaders: true, TrustedProxyCIDRs: []string{"127.0.0.0/8"}}},
			false,
		},
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

// A body sent too slowly fails to read at the deadline; one read in time
// lifts it, so the response may stream for longer.
func TestLimitBodyReadDeadline(t *testing.T) {
	t.Parallel()
	const timeout = 100 * time.Millisecond
	readErr := make(chan error, 1)
	srv := httptest.NewServer(limitBody(1<<20, timeout, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		readErr <- err
		if err != nil {
			return
		}
		time.Sleep(3 * timeout) // a streamed response outlasting the deadline
		_, _ = io.WriteString(w, "streamed")
	})))
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = io.WriteString(conn, "POST /mcp HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\nslow")
	select {
	case err := <-readErr:
		if err == nil {
			t.Fatal("a body sent too slowly was read")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the read deadline did not apply")
	}

	res, err := http.Post(srv.URL+"/mcp", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if body, err := io.ReadAll(res.Body); err != nil || string(body) != "streamed" {
		t.Fatalf("a response after the body was read: %q %v", body, err)
	}
}

func TestLimitBodyBoundary(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "below limit", body: "1234567"},
		{name: "at limit", body: "12345678"},
		{name: "above limit", body: "123456789", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var readErr error
			next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				_, readErr = io.ReadAll(r.Body)
			})
			req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(tc.body))
			limitBody(8, 0, next).ServeHTTP(httptest.NewRecorder(), req)
			if (readErr != nil) != tc.wantErr {
				t.Fatalf("read error = %v, wantErr %v", readErr, tc.wantErr)
			}
		})
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
	if same(identities.lookup("session-1", sessionIdentity{role: "reader"})) {
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

// The session is bound as soon as the initialize response's headers go out:
// a request on the session that arrives while the initialize handler still
// runs (the client sends notifications/initialized as soon as it has the
// session ID) is the same identity's and must not be refused.
func TestBindSessionIdentityBindsBeforeHandlerReturns(t *testing.T) {
	store := newSessionIdentityStore()
	headersOut, release := make(chan struct{}), make(chan struct{})
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(sessionIDHeader) != "" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set(sessionIDHeader, "session-1")
		w.WriteHeader(http.StatusOK)
		http.NewResponseController(w).Flush() //nolint:errcheck // a recorder flushes
		close(headersOut)
		<-release
	})
	handler := withRequestSubject(bindSessionIdentity(store, next))
	done := make(chan struct{})
	go func() {
		defer close(done)
		create := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		create.Header.Set("X-MCP-Role", "reader")
		handler.ServeHTTP(httptest.NewRecorder(), create)
	}()
	<-headersOut
	follow := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	follow.Header.Set(sessionIDHeader, "session-1")
	follow.Header.Set("X-MCP-Role", "reader")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, follow)
	close(release)
	<-done
	if rec.Code != http.StatusAccepted {
		t.Fatalf("request during initialize code = %d, want 202", rec.Code)
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
			if !same(store.lookup("session-1", identity)) {
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
	cases := []struct {
		name    string
		subject string
	}{
		{name: "invalid JSON", subject: `{bad`},
		{name: "array", subject: `[]`},
		{name: "null", subject: `null`},
		{name: "trailing JSON", subject: `{"tenant":7} {}`},
		{name: "object attribute", subject: `{"tenant":{"id":7}}`},
		{name: "nested list", subject: `{"regions":[["eu"]]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("malformed subject must not reach handler")
			})
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			req.Header.Set("X-MCP-Subject", tc.subject)
			rec := httptest.NewRecorder()
			withRequestSubject(next).ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("code = %d, want 400", rec.Code)
			}
		})
	}
}

func TestWithRequestSubjectPreservesLargeInteger(t *testing.T) {
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, subject := subjectFromContext(r.Context(), "reader")
		if got := subject["tenant"]; got != int64(9007199254740993) {
			t.Fatalf("tenant = %#v", got)
		}
		if got := subject["rate"]; got != "0.125" {
			t.Fatalf("rate = %#v, want its literal text", got)
		}
		if got := subject["regions"]; !reflect.DeepEqual(got, []any{"eu", int64(2)}) {
			t.Fatalf("regions = %#v", got)
		}
	})
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("X-MCP-Subject", `{"tenant":9007199254740993,"rate":0.125,"regions":["eu",2]}`)
	withRequestSubject(next).ServeHTTP(httptest.NewRecorder(), req)
}

func TestTrustedProxyAddress(t *testing.T) {
	networks, err := parseTrustedProxyCIDRs([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := trustedProxy(networks, nil, false, next, next)
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

// Under mTLS only a certificate named in the trusted subjects (CN or DNS
// SAN) is a proxy; any other certificate is a caller: it is served without
// identity headers and refused with them (TM-005).
func TestTrustedProxyCertificate(t *testing.T) {
	t.Parallel()
	networks, err := parseTrustedProxyCIDRs([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	withCert := func(cn string, dns ...string) *tls.ConnectionState {
		return &tls.ConnectionState{PeerCertificates: []*x509.Certificate{
			{Subject: pkix.Name{CommonName: cn}, DNSNames: dns},
		}}
	}
	cases := []struct {
		name   string
		mtls   bool
		tls    *tls.ConnectionState
		remote string
		role   string
		want   string // which handler served it, or "403"
	}{
		{"proxy by CN", true, withCert("gateway"), "192.0.2.1:1", "reader", "proxied"},
		{"proxy by DNS SAN", true, withCert("x", "gw.internal"), "192.0.2.1:1", "reader", "proxied"},
		{"proxy by address under mTLS", true, withCert("alice"), "10.1.1.1:1", "reader", "proxied"},
		{"caller certificate without headers", true, withCert("alice"), "192.0.2.1:1", "", "direct"},
		{"caller certificate with headers", true, withCert("alice"), "192.0.2.1:1", "reader", "403"},
		{"caller certificate naming the proxy in a SAN it lacks", true, withCert("gateway.evil"), "192.0.2.1:1",
			"reader", "403"},
		{"no mTLS, untrusted address without headers", false, nil, "192.0.2.1:1", "", "403"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			served := "403"
			mark := func(name string) http.Handler {
				return http.HandlerFunc(func(http.ResponseWriter, *http.Request) { served = name })
			}
			handler := trustedProxy(networks, []string{"gateway", "gw.internal"}, tc.mtls, mark("proxied"),
				mark("direct"))
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			req.RemoteAddr, req.TLS = tc.remote, tc.tls
			if tc.role != "" {
				req.Header.Set("X-MCP-Role", tc.role)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if served != tc.want || (tc.want == "403" && rec.Code != http.StatusForbidden) {
				t.Fatalf("served by %s (code %d), want %s", served, rec.Code, tc.want)
			}
		})
	}
}

func TestTrustedProxyRejectsForgedIdentityFromUntrustedSource(t *testing.T) {
	networks, err := parseTrustedProxyCIDRs([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		role    string
		subject string
	}{
		{name: "forged role", role: "admin"},
		{name: "forged subject", subject: `{"tenant":7}`},
		{name: "forged role and subject", role: "admin", subject: `{"tenant":7}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reached := false
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })
			handler := trustedProxy(networks, nil, false, withRequestSubject(next), next)
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			req.RemoteAddr = "192.0.2.1:1234"
			req.Header.Set("X-MCP-Role", tc.role)
			req.Header.Set("X-MCP-Subject", tc.subject)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("code = %d, want 403", rec.Code)
			}
			if reached {
				t.Fatal("forged identity reached trusted subject middleware")
			}
		})
	}
}

// TestHealthAndReadinessEndpoints guards the health separation contract:
// /healthz is liveness only and always 200; /readyz/snapshot and /readyz/db
// reflect their probes and fail closed (503) when a probe is missing or
// failing, without echoing probe error details.
func TestHealthAndReadinessEndpoints(t *testing.T) {
	t.Parallel()
	probeErr := errors.New("db down: dsn=postgres://user:secret@host/db")
	cases := []struct {
		name     string
		cfg      HTTPConfig
		path     string
		wantCode int
	}{
		{"liveness always ok", HTTPConfig{}, "/healthz", http.StatusOK},
		{"snapshot probe missing fails closed", HTTPConfig{}, "/readyz/snapshot", http.StatusServiceUnavailable},
		{"db probe missing fails closed", HTTPConfig{}, "/readyz/db", http.StatusServiceUnavailable},
		{
			"snapshot ready",
			HTTPConfig{SnapshotReady: func(context.Context) error { return nil }},
			"/readyz/snapshot", http.StatusOK,
		},
		{
			"snapshot not ready",
			HTTPConfig{SnapshotReady: func(context.Context) error { return probeErr }},
			"/readyz/snapshot", http.StatusServiceUnavailable,
		},
		{
			"db ready",
			HTTPConfig{DatabaseReady: func(context.Context) error { return nil }},
			"/readyz/db", http.StatusOK,
		},
		{
			"db not ready",
			HTTPConfig{DatabaseReady: func(context.Context) error { return probeErr }},
			"/readyz/db", http.StatusServiceUnavailable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := buildHTTPMux(http.NotFoundHandler(), nil, tc.cfg)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != tc.wantCode {
				t.Fatalf("%s code = %d, want %d", tc.path, rec.Code, tc.wantCode)
			}
			if strings.Contains(rec.Body.String(), "secret") {
				t.Fatalf("readiness body leaks probe detail: %s", rec.Body.String())
			}
		})
	}
}

// TestReadinessProbeReceivesBoundedContext ensures a hung probe cannot stall
// the endpoint forever: the handler passes a deadline-bounded context.
func TestReadinessProbeReceivesBoundedContext(t *testing.T) {
	t.Parallel()
	var hasDeadline bool
	h := readinessHandler(func(ctx context.Context) error {
		_, hasDeadline = ctx.Deadline()
		return nil
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz/db", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if !hasDeadline {
		t.Fatal("probe context must carry a deadline")
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
	want := "procedure_refresh-cache"
	for _, registered := range list.Tools {
		if registered.Name == want {
			return
		}
	}
	t.Fatalf("custom procedure tool %q not registered", want)
}

func TestSnapshotReadinessMarksStaleRevision(t *testing.T) {
	t.Parallel()
	var stale int64
	mux := buildHTTPMux(http.NotFoundHandler(), nil, HTTPConfig{
		SnapshotReady: func(context.Context) error { return nil },
		SnapshotStale: func() int64 { return stale },
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz/snapshot", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("X-Snapshot-Stale") != "" {
		t.Fatalf("fresh snapshot: code=%d header=%q", rec.Code, rec.Header().Get("X-Snapshot-Stale"))
	}
	stale = 42
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz/snapshot", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("X-Snapshot-Stale") != "42" {
		t.Fatalf("stale snapshot must stay ready and be marked: code=%d header=%q",
			rec.Code, rec.Header().Get("X-Snapshot-Stale"))
	}
}

// same is whether a session is bound to the identity looked up.
func same(_, same bool) bool { return same }
