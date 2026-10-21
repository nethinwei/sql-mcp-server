package mcpserver

import (
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
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

// A proxy's DNS SAN matches its configured subject in any case, as DNS
// names do; its CN matches exactly, as a user's does.
func TestProxyCertificateMatchesDNSNamesInAnyCase(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		subjects []string
		cert     x509.Certificate
		want     bool
	}{
		{[]string{"gateway.example.com"}, x509.Certificate{DNSNames: []string{"Gateway.Example.COM"}}, true},
		{[]string{"Gateway.example.com"}, x509.Certificate{DNSNames: []string{"gateway.example.com"}}, true},
		{[]string{"gateway.example.com"}, x509.Certificate{DNSNames: []string{"other.example.com"}}, false},
		{[]string{"gateway"}, x509.Certificate{Subject: pkix.Name{CommonName: "gateway"}}, true},
		{[]string{"gateway"}, x509.Certificate{Subject: pkix.Name{CommonName: "Gateway"}}, false},
	} {
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{&c.cert}}
		if got := fromProxyCertificate(c.subjects, nil, req); got != c.want {
			t.Errorf("subjects %v, DNS %v, CN %q: %v, want %v", c.subjects, c.cert.DNSNames,
				c.cert.Subject.CommonName, got, c.want)
		}
	}
}

// uriUsers names a user by a certificate's URI SAN, as a uri: certSubjects
// entry does.
type uriUsers struct {
	fakeUsers
	uri, name string
}

func (u uriUsers) UserByCertificate(leaf *x509.Certificate) (bootstrap.UserIdentity, bool) {
	for _, uri := range leaf.URIs {
		if uri.String() == u.uri {
			return u.UserByName(u.name)
		}
	}
	return bootstrap.UserIdentity{}, false
}

// A user's certificate (here by a uri: subject, which the configuration
// cannot compare with a proxy's names) that also carries a proxy's DNS name
// is not the proxy's: its holder must not name other callers by headers.
func TestUserCertificateIsNoProxy(t *testing.T) {
	t.Parallel()
	alice, _ := url.Parse("spiffe://example.org/agent/alice")
	users := uriUsers{fakeUsers: testUsers(), uri: alice.String(), name: "alice"}
	subjects := []string{"gateway.example.com"}
	cert := &x509.Certificate{DNSNames: []string{"gateway.example.com"}, URIs: []*url.URL{alice}}
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	if fromProxyCertificate(subjects, users, req) {
		t.Fatal("a user's certificate was trusted as a proxy's")
	}
	if !fromProxyCertificate(subjects, nil, req) {
		t.Fatal("without that user the certificate is the proxy's")
	}
	served := ""
	mark := func(name string) http.Handler {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) { served = name })
	}
	req.Header.Set(userHeader, "bob")
	rec := httptest.NewRecorder()
	trustedProxy(nil, subjects, users, true, mark("proxied"), mark("direct")).ServeHTTP(rec, req)
	if served != "" || rec.Code != http.StatusForbidden {
		t.Fatalf("identity headers from a user's certificate: served %q, %d", served, rec.Code)
	}
}
