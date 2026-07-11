package main

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/version"
)

func TestResolveServeEndpointUsesConfigUnlessFlagExplicit(t *testing.T) {
	cfg := &config.Config{Server: config.ServerConfig{Transport: "http", Addr: "127.0.0.1:9090"}}
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	transport := fs.String("transport", "stdio", "")
	addr := fs.String("addr", ":8080", "")
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	resolveServeEndpoint(fs, cfg, transport, addr)
	if *transport != "http" || *addr != "127.0.0.1:9090" {
		t.Fatalf("config endpoint = %q %q", *transport, *addr)
	}

	fs = flag.NewFlagSet("serve", flag.ContinueOnError)
	transport = fs.String("transport", "stdio", "")
	addr = fs.String("addr", ":8080", "")
	if err := fs.Parse([]string{"--transport=stdio", "--addr=:7070"}); err != nil {
		t.Fatal(err)
	}
	resolveServeEndpoint(fs, cfg, transport, addr)
	if *transport != "stdio" || *addr != ":7070" {
		t.Fatalf("explicit endpoint = %q %q", *transport, *addr)
	}
}

func TestValidateHotReloadConfigRejectsListenerSecurityAndTools(t *testing.T) {
	server := config.ServerConfig{Transport: "http", Addr: ":8080", Auth: config.AuthConfig{Token: "a"}}
	tools := config.ExplicitToolFlags(config.DefaultToolFlags())
	next := &config.Config{Server: server, Tools: tools}
	if err := validateHotReloadConfig(server, tools, next); err != nil {
		t.Fatal(err)
	}
	next.Server.Auth.Token = "b"
	if err := validateHotReloadConfig(server, tools, next); err == nil {
		t.Fatal("auth change must require restart")
	}
	next.Server = server
	next.Tools.ReadRecords = false
	if err := validateHotReloadConfig(server, tools, next); err == nil {
		t.Fatal("tool-set change must require restart")
	}
}

func TestParseCommandPreservesLegacyFlags(t *testing.T) {
	command, args := parseCommand([]string{"--config", "custom.yaml"})
	if command != "serve" || len(args) != 2 || args[0] != "--config" {
		t.Fatalf("parseCommand = %q, %v", command, args)
	}
	command, args = parseCommand([]string{"validate", "--config", "custom.yaml"})
	if command != "validate" || len(args) != 2 {
		t.Fatalf("parseCommand = %q, %v", command, args)
	}
}

func TestVersionCommand(t *testing.T) {
	var out bytes.Buffer
	if err := runCLI(context.Background(), []string{"version"}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != version.String() {
		t.Fatalf("version output = %q", out.String())
	}
}

func TestInitWritesCurrentConfigVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := runInit([]string{"--config", path, "--driver", "postgres"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "version: \"1\"\n") {
		t.Fatalf("config = %q, want version 1", data)
	}
}

func TestExportIsDeterministicAndPreservesSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	// Map-typed sections (fieldACL, rowPolicies) with multiple keys exercise
	// deterministic key ordering; the DSN placeholder must survive verbatim.
	source := []byte(`version: "1"
database:
  driver: postgres
  dsn: ${EXPORT_TEST_DSN}
entities:
  - name: users
    source: users
    primaryKey: [id]
    fields:
      - name: id
      - name: email
        mask: email
      - name: tenant_id
    roles:
      read: [reader]
    fieldACL:
      reader: {read: [id, email]}
      operator: {read: [id]}
    rowPolicies:
      reader: {op: eq, field: tenant_id, value: "${subject.tenant_id}"}
      operator: {op: eq, field: tenant_id, value: 1}
`)
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	var first, second bytes.Buffer
	if err := runCLI(context.Background(), []string{"export", "--config", path}, &first); err != nil {
		t.Fatal(err)
	}
	if err := runCLI(context.Background(), []string{"export", "--config", path}, &second); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatalf("export is not deterministic:\n--- first ---\n%s\n--- second ---\n%s", &first, &second)
	}
	if !strings.Contains(first.String(), "${EXPORT_TEST_DSN}") {
		t.Fatalf("export must preserve secret placeholders verbatim:\n%s", &first)
	}
}

func TestExportRoundTripsThroughValidate(t *testing.T) {
	t.Setenv("EXPORT_RT_DSN", "postgres://localhost/test")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	source := []byte("database:\n  driver: postgres\n  dsn: ${EXPORT_RT_DSN}\nentities: []\n")
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	var exported bytes.Buffer
	if err := runCLI(context.Background(), []string{"export", "--config", path}, &exported); err != nil {
		t.Fatal(err)
	}
	roundTrip := filepath.Join(dir, "exported.yaml")
	if err := os.WriteFile(roundTrip, exported.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runCLI(context.Background(), []string{"validate", "--config", roundTrip}, &out); err != nil {
		t.Fatalf("exported config failed validate: %v\n%s", err, &exported)
	}
	var again bytes.Buffer
	if err := runCLI(context.Background(), []string{"export", "--config", roundTrip}, &again); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(exported.Bytes(), again.Bytes()) {
		t.Fatalf("export of exported config drifted:\n--- first ---\n%s\n--- second ---\n%s", &exported, &again)
	}
}

func TestValidateCommandResolvesSecrets(t *testing.T) {
	t.Setenv("CLI_TEST_DSN", "postgres://localhost/test")
	path := filepath.Join(t.TempDir(), "config.yaml")
	config := []byte("database:\n  driver: postgres\n  dsn: ${CLI_TEST_DSN}\nentities: []\n")
	if err := os.WriteFile(path, config, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runCLI(context.Background(), []string{"validate", "--config", path}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "valid" {
		t.Fatalf("validate output = %q", out.String())
	}
	t.Setenv("CLI_TEST_DSN", "")
	if err := os.Unsetenv("CLI_TEST_DSN"); err != nil {
		t.Fatal(err)
	}
	if err := runCLI(context.Background(), []string{"validate", "--config", path}, &out); err == nil {
		t.Fatal("validate should reject an unresolved secret")
	}
}
