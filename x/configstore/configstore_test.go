package configstore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/core/revision/revisiontest"
)

func sqliteSpec(t *testing.T) Spec {
	t.Helper()
	return Spec{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "config.db")}
}

func TestSQLiteStoreConformance(t *testing.T) {
	revisiontest.Run(t, func(t *testing.T) revision.Store {
		spec := sqliteSpec(t)
		if err := Init(context.Background(), spec, nil); err != nil {
			t.Fatal(err)
		}
		store, err := Open(context.Background(), spec, nil)
		if err != nil {
			t.Fatal(err)
		}
		return store
	})
}

func TestOpenRequiresInitAndKnownSchema(t *testing.T) {
	ctx := context.Background()
	spec := sqliteSpec(t)
	if _, err := Open(ctx, spec, nil); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("Open on an empty database err = %v", err)
	}
	if err := Init(ctx, spec, nil); err != nil {
		t.Fatal(err)
	}
	if err := Init(ctx, spec, nil); err != nil {
		t.Fatalf("Init must be idempotent: %v", err)
	}
	store, err := Open(ctx, spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.exec(ctx, store.db, "UPDATE smcp_store_meta SET meta_value = ? WHERE meta_key = ?",
		SchemaVersion+1, metaSchemaVersion); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	if _, err := Open(ctx, spec, nil); !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("Open on a newer schema err = %v", err)
	}
	if err := Init(ctx, spec, nil); !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("Init on a newer schema err = %v", err)
	}
}

func TestOpenResolvesDSN(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "resolved.db")
	resolve := func(dsn string) (string, error) {
		if dsn != "${STORE_PATH}" {
			t.Fatalf("resolver got %q", dsn)
		}
		return path, nil
	}
	spec := Spec{Driver: "sqlite", DSN: "${STORE_PATH}"}
	if err := Init(ctx, spec, resolve); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, Spec{Driver: "sqlite", DSN: path}, nil)
	if err != nil {
		t.Fatalf("store must live at the resolved path: %v", err)
	}
	_ = store.Close()
	failing := func(string) (string, error) { return "", errors.New("boom") }
	if _, err := Open(ctx, spec, failing); err == nil {
		t.Fatal("resolver errors must fail Open")
	}
}

func TestParseSpec(t *testing.T) {
	t.Parallel()
	got, err := ParseSpec(" postgres:postgres://u@h/db?sslmode=disable ")
	if err != nil || got.Driver != "postgres" || got.DSN != "postgres://u@h/db?sslmode=disable" {
		t.Fatalf("ParseSpec = %+v, %v", got, err)
	}
	if got.String() != "postgres:<redacted>" {
		t.Fatalf("String must not leak the DSN: %q", got.String())
	}
	for _, raw := range []string{"", "sqlite", "sqlite:", "oracle:x"} {
		if _, err := ParseSpec(raw); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("ParseSpec(%q) err = %v", raw, err)
		}
	}
}

func TestDialectHelpers(t *testing.T) {
	t.Parallel()
	if got := dialects["postgres"].rebind("a = ? AND b = ?"); got != "a = $1 AND b = $2" {
		t.Fatalf("postgres rebind = %q", got)
	}
	if got := dialects["mysql"].rebind("a = ?"); got != "a = ?" {
		t.Fatalf("mysql rebind = %q", got)
	}
	if got := sqliteDSN("/tmp/x.db"); got != "file:/tmp/x.db?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)" {
		t.Fatalf("sqliteDSN = %q", got)
	}
	want := "file:x.db?mode=rwc&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
	if got := sqliteDSN("file:x.db?mode=rwc"); got != want {
		t.Fatalf("sqliteDSN with query = %q", got)
	}
}
