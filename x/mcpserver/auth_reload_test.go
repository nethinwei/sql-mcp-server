package mcpserver

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// writeCert writes a self-signed certificate named cn and its key, returning
// their paths.
func writeCert(t *testing.T, cn string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		DNSNames: []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	write := func(path, kind string, der []byte) {
		if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(certPath, "CERTIFICATE", der)
	write(keyPath, "EC PRIVATE KEY", keyDER)
	return certPath, keyPath
}

func prepared(t *testing.T, auth HTTPAuth) PreparedAuth {
	t.Helper()
	p, err := PrepareHTTPAuth("127.0.0.1:0", auth)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// An authentication whose certificate cannot be read is not prepared: the
// reload preparing it fails before anything is published.
func TestPrepareHTTPAuthReadsCertificates(t *testing.T) {
	t.Parallel()
	certPath, keyPath := writeCert(t, "gone")
	_ = os.Remove(certPath)
	if _, err := PrepareHTTPAuth("127.0.0.1:0", HTTPAuth{Token: "t", TLSCert: certPath, TLSKey: keyPath}); err == nil {
		t.Fatal("preparing with an unreadable certificate must fail")
	}
}

func status(t *testing.T, h http.Handler, token string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// A new token applies to the next request, without a restart.
func TestAuthChangesRotateTheToken(t *testing.T) {
	t.Parallel()
	var apply func(PreparedAuth) error
	h, err := Handler(mcp.NewServer(&mcp.Implementation{Name: "t"}, nil), HTTPConfig{
		Addr: "127.0.0.1:0", Token: "old", AuthChanges: func(fn func(PreparedAuth) error) { apply = fn },
	})
	if err != nil {
		t.Fatal(err)
	}
	if status(t, h, "old") == http.StatusUnauthorized {
		t.Fatal("the configured token must be accepted")
	}
	if err := apply(prepared(t, HTTPAuth{Token: "new"})); err != nil {
		t.Fatal(err)
	}
	if status(t, h, "old") != http.StatusUnauthorized || status(t, h, "new") == http.StatusUnauthorized {
		t.Fatal("after the change only the new token may be accepted")
	}
}

// A rotated certificate serves the next handshake; switching TLS off is
// refused and keeps the current settings.
func TestAuthChangesRotateTheCertificate(t *testing.T) {
	t.Parallel()
	firstCert, firstKey := writeCert(t, "first")
	secondCert, secondKey := writeCert(t, "second")
	cfg := HTTPConfig{Addr: "127.0.0.1:0", Token: "t", TLSCert: firstCert, TLSKey: firstKey}
	h, err := newHTTPServing(mcp.NewServer(&mcp.Implementation{Name: "t"}, nil), cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(h.handler())
	srv.TLS = &tls.Config{GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) { return h.tls.Load(), nil }}
	srv.StartTLS()
	defer srv.Close()
	served := func() string {
		conn, err := tls.Dial("tcp", srv.Listener.Addr().String(), &tls.Config{InsecureSkipVerify: true})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		return conn.ConnectionState().PeerCertificates[0].Subject.CommonName
	}
	if got := served(); got != "first" {
		t.Fatalf("served %q", got)
	}
	// Prepared, the certificate is read: switching to it reads nothing, so
	// it serves even once the file is gone.
	rotated := prepared(t, HTTPAuth{Token: "t", TLSCert: secondCert, TLSKey: secondKey})
	_ = os.Remove(secondCert)
	if err := h.apply(rotated); err != nil {
		t.Fatal(err)
	}
	if got := served(); got != "second" {
		t.Fatalf("after rotation served %q", got)
	}
	if err := h.apply(prepared(t, HTTPAuth{Token: "t"})); err == nil {
		t.Fatal("switching TLS off while serving must be refused")
	}
	if got := served(); got != "second" {
		t.Fatalf("a refused change must keep the current certificate, served %q", got)
	}
}

// newClientCA writes a CA certificate and returns its path with a client
// certificate it signed.
func newClientCA(t *testing.T) (string, tls.Certificate) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ca := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ca"}, IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
	}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	clientKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	client := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "client"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		NotBefore:   time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, client, ca, &clientKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, tls.Certificate{Certificate: [][]byte{clientDER}, PrivateKey: clientKey}
}

// Switching to mTLS while serving: a connection opened before, without a
// client certificate, is refused on its next request instead of passing for
// mTLS-authenticated; a new connection with a certificate the CA signed is
// served.
func TestAuthChangesToMTLSRefuseOldConnections(t *testing.T) {
	t.Parallel()
	certPath, keyPath := writeCert(t, "server")
	caPath, clientCert := newClientCA(t)
	h, err := newHTTPServing(mcp.NewServer(&mcp.Implementation{Name: "t"}, nil), HTTPConfig{
		Addr: "127.0.0.1:0", Token: "t", TLSCert: certPath, TLSKey: keyPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(h.handler())
	srv.TLS = &tls.Config{GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) { return h.tls.Load(), nil }}
	srv.StartTLS()
	defer srv.Close()
	post := func(c *http.Client, token string) (int, bool) {
		reused := false
		trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused }}
		req, _ := http.NewRequestWithContext(httptrace.WithClientTrace(context.Background(), trace),
			http.MethodPost, srv.URL+"/mcp", strings.NewReader("{}"))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
		return res.StatusCode, reused
	}
	old := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	if code, _ := post(old, "t"); code == http.StatusUnauthorized {
		t.Fatal("the token must be accepted before the change")
	}
	if err := h.apply(prepared(t, HTTPAuth{TLSCert: certPath, TLSKey: keyPath, ClientCA: caPath})); err != nil {
		t.Fatal(err)
	}
	if code, reused := post(old, ""); !reused || code != http.StatusUnauthorized {
		t.Fatalf("old connection without a client certificate: status %d (reused %v)", code, reused)
	}
	withCert := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		InsecureSkipVerify: true, Certificates: []tls.Certificate{clientCert},
	}}}
	if code, _ := post(withCert, ""); code == http.StatusUnauthorized {
		t.Fatal("a client certificate the CA signed must be accepted")
	}
}
