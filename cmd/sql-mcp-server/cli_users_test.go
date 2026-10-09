package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
)

func TestUserTokenPrintsTokenAndMatchingHash(t *testing.T) {
	var out bytes.Buffer
	if err := runCLI(context.Background(), []string{"user", "token"}, &out); err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`^token: (smcp_[A-Za-z0-9_-]{43})\ntokenHash: (sha256:[0-9a-f]{64})\n$`).
		FindStringSubmatch(out.String())
	if m == nil {
		t.Fatalf("unexpected output:\n%s", &out)
	}
	if config.TokenHash(m[1]) != m[2] {
		t.Fatal("printed tokenHash does not match the token")
	}
	var again bytes.Buffer
	if err := runCLI(context.Background(), []string{"user", "token"}, &again); err != nil {
		t.Fatal(err)
	}
	if again.String() == out.String() {
		t.Fatal("tokens must be random")
	}
	if err := runCLI(context.Background(), []string{"user"}, &out); err == nil {
		t.Fatal("user without a subcommand must fail")
	}
}

func TestApplyUserOverride(t *testing.T) {
	cfg := &config.Config{Users: map[string]config.UserConfig{"alice": {}, "bob": {Disabled: true}}}
	if err := applyUserOverride(cfg, " Alice "); err != nil || cfg.Server.User != "alice" {
		t.Fatalf("override = %q, %v", cfg.Server.User, err)
	}
	if err := applyUserOverride(cfg, "ghost"); err == nil {
		t.Fatal("unknown --user must fail")
	}
	if err := applyUserOverride(cfg, "bob"); err == nil {
		t.Fatal("disabled --user must fail")
	}
	if err := applyUserOverride(cfg, ""); err != nil || cfg.Server.User != "alice" {
		t.Fatal("empty --user must keep the config value")
	}
}

const usersConfigYAML = `version: "1"
database:
  driver: postgres
  dsn: ${USERS_TEST_DSN}
entities:
  - name: orders
    fields:
      - name: id
      - name: tenant_id
    tenantPolicy: {op: eq, field: tenant_id, value: "${subject.tenant_id}"}
roles:
  analyst:
    grants:
      - entity: orders
        actions: [read]
users:
  alice:
    tokenHash: sha256:0000000000000000000000000000000000000000000000000000000000000000
    roles: [analyst]
    subject: {tenant_id: t1}
`

// Configuring the first user is a reload like any other: the HTTP transport
// follows whether users are configured.
func TestReloadTogglesUsers(t *testing.T) {
	t.Setenv("USERS_TEST_DSN", "postgres://localhost/test")
	next, err := bootstrap.LoadBytes([]byte(usersConfigYAML))
	if err != nil {
		t.Fatal(err)
	}
	startup, err := bootstrap.LoadBytes([]byte(
		"database:\n  driver: postgres\n  dsn: ${USERS_TEST_DSN}\nentities:\n  - name: orders\n"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = serveBuilder(startup, serveOverrides{}, nil, bootstrap.NewShared(), nil)(next)
	if errors.Is(err, bootstrap.ErrRestartRequired) {
		t.Fatalf("enabling users by reload must not require a restart: %v", err)
	}
}

func TestExportOmitsUnusedAccessFieldsAndRoundTripsUsers(t *testing.T) {
	t.Setenv("USERS_TEST_DSN", "postgres://localhost/test")
	dir := t.TempDir()
	legacy := filepath.Join(dir, "legacy.yaml")
	source := "database:\n  driver: postgres\n  dsn: ${USERS_TEST_DSN}\nentities:\n  - name: orders\n"
	if err := os.WriteFile(legacy, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runCLI(context.Background(), []string{"export", "--config", legacy}, &out); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"\nroles:", "\nusers:", "tenantPolicy", "  user:", "  users:"} {
		if strings.Contains(out.String(), key) {
			t.Fatalf("legacy export must not gain %q:\n%s", key, &out)
		}
	}

	path := filepath.Join(dir, "users.yaml")
	if err := os.WriteFile(path, []byte(usersConfigYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	var first bytes.Buffer
	if err := runCLI(context.Background(), []string{"export", "--config", path}, &first); err != nil {
		t.Fatal(err)
	}
	roundTrip := filepath.Join(dir, "exported.yaml")
	if err := os.WriteFile(roundTrip, first.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	var second bytes.Buffer
	if err := runCLI(context.Background(), []string{"export", "--config", roundTrip}, &second); err != nil {
		t.Fatalf("exported users config failed to load: %v\n%s", err, &first)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatalf("users export drifted:\n--- first ---\n%s\n--- second ---\n%s", &first, &second)
	}
	for _, key := range []string{"tenantPolicy:", "\nroles:", "\nusers:", "tokenHash: sha256:"} {
		if !strings.Contains(first.String(), key) {
			t.Fatalf("users export must contain %q:\n%s", key, &first)
		}
	}
}
