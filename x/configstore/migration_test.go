package configstore

import (
	"context"
	"testing"

	"github.com/nethinwei/sql-mcp-server/x/catalog"
)

func TestSQLiteMigratesVersionThree(t *testing.T) {
	assertMigratesVersionThree(t, sqliteSpec(t))
}

// assertMigratesVersionThree opens a store of schema version 3 holding a
// token and a scan batch (its items in its payload): the token reads with no
// scope, the batch keeps its items, and both work as written now.
func assertMigratesVersionThree(t *testing.T, spec Spec) {
	t.Helper()
	ctx := context.Background()
	versionThreeStore(t, spec)
	store, err := Open(ctx, spec, nil)
	if err != nil {
		t.Fatalf("Open must migrate a version 3 store: %v", err)
	}
	defer store.Close()
	if version, err := store.checkSchema(ctx); err != nil || version != SchemaVersion {
		t.Fatalf("schema version after migration = %d, %v", version, err)
	}
	tok, ok, err := store.AdminTokenByHash(ctx, "sha256:1")
	if err != nil || !ok || tok.Scope.Datasources != nil || tok.Scope.ReadOnly {
		t.Fatalf("token = %+v %v %v", tok, ok, err)
	}
	assertMigratedBatch(t, store)
}

// versionThreeStore creates the tables of schema version 3 with a token and
// a scan batch.
func versionThreeStore(t *testing.T, spec Spec) {
	t.Helper()
	ctx := context.Background()
	s, err := connect(spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range s.dialect.migrations[:3] {
		for _, ddl := range step {
			if _, err := s.db.ExecContext(ctx, ddl); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO smcp_store_meta (meta_key, meta_value) VALUES (?, ?), (?, ?), (?, ?)",
			[]any{metaSchemaVersion, 3, metaNextID, 1, metaPublishLock, 0}},
		{"INSERT INTO smcp_admin_tokens (id, username, name, token_hash, created_us, expires_us, last_used_us) " +
			"VALUES (?, ?, ?, ?, ?, ?, ?)", []any{"t1", "root", "laptop", "sha256:1", 1, 0, 0}},
		{"INSERT INTO smcp_scan_batches (id, datasource, started_by, owner, started_us, finished_us, heartbeat_us, " +
			"canceled, payload) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)", []any{"b1", "shop", "root", "p1.r1", 1, 0, 1, 0,
			`[{"schema":"a","state":"done"},{"schema":"b","state":"failed","error":"boom"},{"schema":"c","state":"running"}]`}},
	} {
		if _, err := s.exec(ctx, s.db, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.Close()
}

func assertMigratedBatch(t *testing.T, store *SQLStore) {
	t.Helper()
	ctx := context.Background()
	b, ok, err := store.Batch(ctx, "b1")
	if err != nil || !ok || len(b.Items) != 3 || b.Items[1] != (catalog.BatchItem{Schema: "b", State: "failed",
		Error: "boom"}) || b.Items[2].State != catalog.ItemRunning {
		t.Fatalf("batch = %+v %v %v", b, ok, err)
	}
	if moved, err := store.MoveItem(ctx, "b1", "p1.r1", fromMicros(2), catalog.BatchItem{Schema: "c",
		State: catalog.ItemPending}, catalog.ItemRunning); !moved || err != nil {
		t.Fatalf("an item of a migrated batch: %v %v", moved, err)
	}
	if _, created, err := store.CreateBatch(ctx, catalog.Batch{ID: "b2", Datasource: "shop", RequestKey: "k",
		Items: []catalog.BatchItem{{Schema: "a", State: catalog.ItemPending}}}, 4); !created || err != nil {
		t.Fatalf("a batch after migration: %v %v", created, err)
	}
}
