package mcpserver

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
