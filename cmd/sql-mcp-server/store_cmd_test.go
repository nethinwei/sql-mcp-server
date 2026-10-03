package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
)

const storeTestConfig = `database:
  driver: postgres
  dsn: ${STORE_TEST_DSN}
server:
  transport: http
  addr: 127.0.0.1:8080
entities:
  - name: orders
    fields:
      - name: id
cost:
  maxRows: %d
`

func writeStoreConfig(t *testing.T, dir, name string, maxRows int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(fmt.Sprintf(storeTestConfig, maxRows)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// runCmd runs the CLI and returns its stdout.
func runCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := runCLI(context.Background(), args, &out)
	return out.String(), err
}

func mustRun(t *testing.T, args ...string) string {
	t.Helper()
	out, err := runCmd(t, args...)
	if err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return out
}

func newTestStore(t *testing.T) (string, string) {
	t.Helper()
	t.Setenv("STORE_TEST_DSN", "postgres://localhost/test")
	dir := t.TempDir()
	spec := "sqlite:" + filepath.Join(dir, "config.db")
	mustRun(t, "store", "init", "--store", spec)
	return dir, spec
}

var draftRe = regexp.MustCompile(`^draft (\d+) sha256:[0-9a-f]{64}\n$`)

func importDraft(t *testing.T, spec, path string) string {
	t.Helper()
	out := mustRun(t, "store", "import", "--store", spec, "--config", path, "--author", "tester")
	m := draftRe.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("import output = %q", out)
	}
	return m[1]
}

func TestStoreCommandsLifecycle(t *testing.T) {
	dir, spec := newTestStore(t)
	first := writeStoreConfig(t, dir, "v1.yaml", 5)
	id1 := importDraft(t, spec, first)
	mustRun(t, "store", "publish", "--store", spec, id1)
	if out := mustRun(t, "store", "import", "--store", spec, "--config", first); !strings.HasPrefix(out, "unchanged") {
		t.Fatalf("re-importing the published config must be a no-op: %q", out)
	}
	id2 := importDraft(t, spec, writeStoreConfig(t, dir, "v2.yaml", 7))
	diff := mustRun(t, "store", "diff", "--store", spec, id2)
	if !strings.Contains(diff, "-  maxRows: 7") || !strings.Contains(diff, "+  maxRows: 5") {
		t.Fatalf("diff from draft to published:\n%s", diff)
	}
	mustRun(t, "store", "publish", "--store", spec, id2)
	list := mustRun(t, "store", "list", "--store", spec)
	if !strings.Contains(list, "published") || !strings.Contains(list, "superseded") || !strings.Contains(list, "tester") {
		t.Fatalf("list:\n%s", list)
	}
	if out := mustRun(t, "store", "rollback", "--store", spec); !strings.Contains(out, "content of "+id1) {
		t.Fatalf("rollback output = %q", out)
	}
	exported := mustRun(t, "export", "--config", first)
	if shown := mustRun(t, "store", "show", "--store", spec, "published"); shown != exported {
		t.Fatalf("rollback must restore the first payload byte for byte:\n%s\nwant:\n%s", shown, exported)
	}
}

func TestStoreImportRejectsPlaintextSecrets(t *testing.T) {
	dir, spec := newTestStore(t)
	plain := filepath.Join(dir, "plain.yaml")
	body := "database:\n  driver: postgres\n  dsn: postgres://app:hunter2@db/app\n"
	if err := os.WriteFile(plain, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runCmd(t, "store", "import", "--store", spec, "--config", plain)
	if !errors.Is(err, bootstrap.ErrPlaintextSecret) {
		t.Fatalf("plaintext DSN password: %v", err)
	}
	token := writeStoreConfig(t, dir, "token.yaml", 5)
	data, _ := os.ReadFile(token)
	data = []byte(strings.Replace(string(data), "  addr: 127.0.0.1:8080\n",
		"  addr: 127.0.0.1:8080\n  auth:\n    token: s3cret\n", 1))
	if err := os.WriteFile(token, data, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = runCmd(t, "store", "import", "--store", spec, "--config", token)
	if !errors.Is(err, bootstrap.ErrPlaintextSecret) {
		t.Fatalf("shared token: %v", err)
	}
}

func TestStorePublishGuardsRestartRequiredChanges(t *testing.T) {
	dir, spec := newTestStore(t)
	mustRun(t, "store", "publish", "--store", spec, importDraft(t, spec, writeStoreConfig(t, dir, "v1.yaml", 5)))
	moved := writeStoreConfig(t, dir, "v2.yaml", 5)
	data, _ := os.ReadFile(moved)
	if err := os.WriteFile(moved, []byte(strings.Replace(string(data), ":8080", ":9090", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	id := importDraft(t, spec, moved)
	if _, err := runCmd(t, "store", "publish", "--store", spec, id); !errors.Is(err, bootstrap.ErrRestartRequired) {
		t.Fatalf("restart-required change must be refused: %v", err)
	}
	mustRun(t, "store", "publish", "--store", spec, "--restart-required", id)
}

func TestMigrateRoundTripsFileAndStores(t *testing.T) {
	dir, spec := newTestStore(t)
	source := writeStoreConfig(t, dir, "v1.yaml", 5)
	mustRun(t, "migrate", "--from", "file:"+source, "--to", spec)
	mustRun(t, "store", "publish", "--store", spec, importDraft(t, spec, writeStoreConfig(t, dir, "v2.yaml", 7)))

	other := "sqlite:" + filepath.Join(dir, "other.db")
	mustRun(t, "store", "init", "--store", other)
	if out := mustRun(t, "migrate", "--from", spec, "--to", other, "--history"); out != "migrated 2 revisions\n" {
		t.Fatalf("history migration output = %q", out)
	}
	back := filepath.Join(dir, "back.yaml")
	mustRun(t, "migrate", "--from", other, "--to", "file:"+back)
	want := mustRun(t, "export", "--config", filepath.Join(dir, "v2.yaml"))
	got, _ := os.ReadFile(back)
	if string(got) != want {
		t.Fatalf("store -> file must reproduce the published payload:\n%s\nwant:\n%s", got, want)
	}
	if _, err := runCmd(t, "migrate", "--from", other, "--to", "file:"+back); err == nil {
		t.Fatal("migrate must not overwrite an existing file")
	}
	if _, err := runCmd(t, "migrate", "--from", spec, "--to", other, "--history"); !errors.Is(err, revision.ErrNotEmpty) {
		t.Fatalf("history into a non-empty store: %v", err)
	}
}

func TestOpenServeSource(t *testing.T) {
	dir, spec := newTestStore(t)
	ctx := context.Background()
	if _, err := openServeSource(ctx, "config.yaml", spec, "", false); err == nil ||
		!strings.Contains(err.Error(), "no published revision") {
		t.Fatalf("empty store: %v", err)
	}
	mustRun(t, "store", "publish", "--store", spec, importDraft(t, spec, writeStoreConfig(t, dir, "v1.yaml", 5)))
	src, err := openServeSource(ctx, "config.yaml", spec, "", false)
	if err != nil {
		t.Fatal(err)
	}
	defer src.close()
	if src.rev == 0 || src.startup.Cost.MaxRows != 5 {
		t.Fatalf("store source = rev %d maxRows %d", src.rev, src.startup.Cost.MaxRows)
	}
	if _, err := openServeSource(ctx, "x.yaml", spec, "", true); err == nil {
		t.Fatal("--config and --store together must fail")
	}
	t.Setenv(storeEnv, spec)
	envSrc, err := openServeSource(ctx, "config.yaml", "", "", false)
	if err != nil || envSrc.store == nil {
		t.Fatalf("SQL_MCP_STORE must select store mode: %v", err)
	}
	envSrc.close()
}
