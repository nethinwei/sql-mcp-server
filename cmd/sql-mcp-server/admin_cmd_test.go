package main

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/x/admin/accounts"
)

// withStdin feeds input to commands that read a password from stdin.
func withStdin(t *testing.T, input string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(input); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old; _ = r.Close() })
}

// The CLI goes through the same account service as the admin API: it
// cannot remove the last account manager either.
func TestAdminCommandsKeepAnAccountManager(t *testing.T) {
	_, spec := newTestStore(t)
	withStdin(t, "a long enough password\n")
	if out := mustRun(t, "admin", "create", "--store", spec, "--username", "Root"); !strings.Contains(
		out,
		"created admin root",
	) {
		t.Fatalf("create: %q", out)
	}
	for _, args := range [][]string{
		{"--disable"},
		{"--permissions", "admin:read"},
	} {
		_, err := runCmd(t, append([]string{"admin", "set", "--store", spec, "--username", "root"}, args...)...)
		if !errors.Is(err, accounts.ErrLastAccountManager) {
			t.Fatalf("admin set %v: err = %v", args, err)
		}
	}
	if out := mustRun(t, "admin", "list", "--store", spec); !strings.Contains(out, "root") ||
		strings.Contains(out, "true") {
		t.Fatalf("root must stay enabled: %q", out)
	}
}

// The console's cookie is Secure whenever browsers reach it over HTTPS:
// served with TLS, or behind a proxy terminating it (an https publicURL).
func TestSecureCookie(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		cert, publicURL string
		want            bool
	}{
		{"", "", false},
		{"cert.pem", "", true},
		{"", "HTTPS://gw.example.com/sql-mcp/mcp", true},
		{"", "http://127.0.0.1:8080/mcp", false},
	} {
		cfg := &config.Config{}
		cfg.Server.Auth.TLS.Cert, cfg.Server.PublicURL = tc.cert, tc.publicURL
		if got := secureCookie(cfg); got != tc.want {
			t.Errorf("cert %q publicURL %q: Secure = %v", tc.cert, tc.publicURL, got)
		}
	}
}
