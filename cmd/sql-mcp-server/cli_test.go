package main

import (
	"bytes"
	"context"
	"errors"
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

func TestAddEntityChecksTheNamespacedIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("entities:\n  - name: orders\n    datasource: main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	add := func(args ...string) error { return runAddEntity(append([]string{"--config", path}, args...)) }
	// Same name, another schema or datasource: another entity.
	if err := add("--name", "orders", "--datasource", "main", "--schema", "archive"); err != nil {
		t.Fatalf("orders in schema archive: %v", err)
	}
	if err := add("--name", "orders", "--datasource", "archive"); err != nil {
		t.Fatalf("orders in datasource archive: %v", err)
	}
	err := add("--name", "orders", "--datasource", "main")
	if err == nil || !strings.Contains(err.Error(), "main.orders") {
		t.Fatalf("duplicate main.orders: %v", err)
	}
	if err := add("--name", "sales.orders"); err == nil {
		t.Fatal("a dotted name was added")
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "schema: archive") {
		t.Fatalf("config = %s", data)
	}
}

func TestExplainResolvesEntityReferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := "databases:\n  main: {driver: postgres, dsn: postgres://x}\nentities:\n" +
		"  - {name: orders, datasource: main, schema: sales, fields: [{name: id}]}\n" +
		"  - {name: orders, datasource: main, schema: archive, fields: [{name: id}]}\n"
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runExplain([]string{"--config", path, "--entity", "archive.orders"}, &out); err != nil ||
		!strings.Contains(out.String(), `"id": "main.archive.orders"`) ||
		strings.Contains(out.String(), "main.sales.orders") {
		t.Fatalf("explain archive.orders: %v %s", err, out.String())
	}
	err := runExplain([]string{"--config", path, "--entity", "orders"}, &out)
	if !errors.Is(err, config.ErrAmbiguousEntity) {
		t.Fatalf("explain orders: %v", err)
	}
}
