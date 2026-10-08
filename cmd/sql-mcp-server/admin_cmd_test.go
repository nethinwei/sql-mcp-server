package main

import (
	"errors"
	"os"
	"strings"
	"testing"

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
