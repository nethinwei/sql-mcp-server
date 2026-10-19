package mcpserver

import (
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// serveCert serves a request whose connection presented a certificate
// with the common name cn (the fake directory names a user by its CN).
func serveCert(h http.Handler, cn, authz string) int {
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{Subject: pkix.Name{CommonName: cn}}}}
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestCertIdentity(t *testing.T) {
	t.Parallel()
	proxy := func(r *http.Request) bool { return r.TLS.PeerCertificates[0].Subject.CommonName == "gateway" }
	cases := []struct {
		name, cn, authz string
		requireUser     bool
		wantCode        int
		wantRole        string
	}{
		{"a mapped certificate is its user", "alice", "", true, http.StatusOK, "user:alice"},
		{"an unmapped certificate is refused", "mallory", "", true, http.StatusUnauthorized, ""},
		{"clientIdentity default keeps the default identity", "mallory", "", false, http.StatusOK, ""},
		{"a bearer token decides instead", "mallory", "Bearer x", true, http.StatusOK, ""},
		{"a trusted proxy is exempt", "gateway", "", true, http.StatusOK, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got requestSubject
			h := certIdentity(testUsers(), tc.requireUser, proxy, identityProbe(&got))
			if code := serveCert(h, tc.cn, tc.authz); code != tc.wantCode || got.role != tc.wantRole {
				t.Fatalf("code=%d role=%q, want %d %q", code, got.role, tc.wantCode, tc.wantRole)
			}
			if tc.wantRole != "" && (!got.user || got.attrs["tenant_id"] != "t1") {
				t.Fatalf("a certificate user must carry its configured subject: %+v", got)
			}
		})
	}
	if h := certIdentity(nil, true, proxy, identityProbe(new(requestSubject))); serveCert(h, "mallory", "") !=
		http.StatusOK {
		t.Fatal("without users, a certificate keeps the default identity")
	}
}

// A certificate user passes principalAuth without a bearer token, even with
// a shared token configured.
func TestCertUserPassesPrincipalAuth(t *testing.T) {
	t.Parallel()
	var got requestSubject
	h := certIdentity(testUsers(), true, func(*http.Request) bool { return false },
		principalAuth("0123456789abcdef0123456789abcdef", testUsers(), true, identityProbe(&got)))
	if code := serveCert(h, "alice", ""); code != http.StatusOK || got.role != "user:alice" {
		t.Fatalf("code=%d role=%q", code, got.role)
	}
}

// Through the served handler chain, under real mTLS: a client certificate
// naming a user is served, one naming none is refused unless clientIdentity
// is "default".
func TestMTLSClientCertificateIdentity(t *testing.T) {
	t.Parallel()
	certPath, keyPath := writeCert(t, "server")
	caPath, clientCert := newClientCA(t) // CN "client"
	for name, c := range map[string]struct {
		users    fakeUsers
		identity string
		want     int
	}{
		"mapped":                   {fakeUsers{"client": {Principal: "user:client"}}, "", http.StatusOK},
		"unmapped":                 {fakeUsers{"alice": {Principal: "user:alice"}}, "", http.StatusUnauthorized},
		"unmapped, default caller": {fakeUsers{"alice": {Principal: "user:alice"}}, "default", http.StatusOK},
	} {
		h, err := newHTTPServing(mcp.NewServer(&mcp.Implementation{Name: "t"}, nil), HTTPConfig{
			Addr: "127.0.0.1:0", HTTPAuth: HTTPAuth{TLSCert: certPath, TLSKey: keyPath, ClientCA: caPath,
				Users: c.users, ClientIdentity: c.identity},
		})
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewUnstartedServer(h.handler())
		srv.TLS = &tls.Config{GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			return h.tls.Load(), nil
		}}
		srv.StartTLS()
		client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, Certificates: []tls.Certificate{clientCert},
		}}}
		res, err := client.Post(srv.URL+"/mcp", "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		srv.Close()
		if got := res.StatusCode == http.StatusUnauthorized; got != (c.want == http.StatusUnauthorized) {
			t.Errorf("%s: status %d", name, res.StatusCode)
		}
	}
}
