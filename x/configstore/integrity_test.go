package configstore

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/revision"
)

func TestSQLiteStoreIntegrity(t *testing.T) {
	t.Run("LockRowRequired", func(t *testing.T) { assertLockRowRequired(t, openSQLite(t)) })
	t.Run("OnePublished", func(t *testing.T) { assertOnePublished(t, openSQLite(t)) })
	t.Run("PublishRaceLeavesOne", func(t *testing.T) { assertConcurrentPublish(t, openSQLite(t)) })
	t.Run("RerunsItsDDL", func(t *testing.T) { assertRerunsItsDDL(t, openSQLite(t)) })
	t.Run("MigrationRefusesTwoPublished", func(t *testing.T) {
		assertMigrationRefusesTwoPublished(t, sqliteSpec(t))
	})
	t.Run("InitCompletesPartialMeta", func(t *testing.T) { assertInitCompletesPartialMeta(t, sqliteSpec(t)) })
}

func draft(t *testing.T, store *SQLStore, payload string) revision.Revision {
	t.Helper()
	r, err := store.Create(context.Background(), revision.Draft{Payload: []byte(payload), Author: "root"})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// assertLockRowRequired: a write whose lock or counter row is missing fails
// instead of running unserialized.
func assertLockRowRequired(t *testing.T, store *SQLStore) {
	t.Helper()
	ctx := context.Background()
	d := draft(t, store, "a: 1\n")
	for _, key := range []string{metaPublishLock, metaNextID} {
		if _, err := store.exec(ctx, store.db, "DELETE FROM smcp_store_meta WHERE meta_key = ?", key); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Publish(ctx, d.ID, 0, revision.Meta{Author: "root"}); !errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("Publish without the lock row: %v", err)
	}
	if _, err := store.Create(ctx, revision.Draft{Payload: []byte("b: 2\n")}); !errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("Create without the id row: %v", err)
	}
}

// assertOnePublished: the database refuses a second published revision.
func assertOnePublished(t *testing.T, store *SQLStore) {
	t.Helper()
	ctx := context.Background()
	a, b := draft(t, store, "a: 1\n"), draft(t, store, "b: 2\n")
	if _, err := store.Publish(ctx, a.ID, 0, revision.Meta{Author: "root"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.exec(ctx, store.db, "UPDATE smcp_revisions SET state = ? WHERE id = ?",
		string(revision.StatePublished), b.ID); err == nil {
		t.Fatal("a second published revision was stored")
	}
}

// assertConcurrentPublish: of drafts published at once, exactly one is.
func assertConcurrentPublish(t *testing.T, store *SQLStore) {
	t.Helper()
	ctx := context.Background()
	drafts := []revision.Revision{draft(t, store, "a: 1\n"), draft(t, store, "b: 2\n"), draft(t, store, "c: 3\n")}
	var wg sync.WaitGroup
	for _, d := range drafts {
		wg.Go(func() { _, _ = store.Publish(ctx, d.ID, 0, revision.Meta{Author: "root"}) })
	}
	wg.Wait()
	list, err := store.List(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	published := 0
	for _, r := range list {
		if r.State == revision.StatePublished {
			published++
		}
	}
	if published != 1 {
		t.Fatalf("%d revisions published", published)
	}
}

// assertMigrationRefusesTwoPublished: a version 5 store holding two
// published revisions does not migrate; it says which, and how to repair.
func assertMigrationRefusesTwoPublished(t *testing.T, spec Spec) {
	t.Helper()
	ctx := context.Background()
	s, err := connect(spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range s.dialect.migrations[:5] {
		for _, ddl := range step {
			if _, err := s.db.ExecContext(ctx, ddl); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := s.exec(ctx, s.db, "INSERT INTO smcp_store_meta (meta_key, meta_value) VALUES "+
		"(?, ?), (?, ?), (?, ?), (?, ?)", metaSchemaVersion, 5, metaNextID, 3, metaPublishLock, 0,
		metaRevisionSeq, 0); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{1, 2} {
		if _, err := s.exec(ctx, s.db, "INSERT INTO smcp_revisions ("+revisionColumns+
			") VALUES (?, 0, 'h', 'x: 1', 'published', 'root', '', 0, 0)", id); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.Close()
	_, err = Open(ctx, spec, nil)
	if !errors.Is(err, ErrStoreCorrupt) || !strings.Contains(err.Error(), "1, 2") {
		t.Fatalf("Open with two published revisions: %v", err)
	}
	s, err = connect(spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.exec(ctx, s.db, "UPDATE smcp_revisions SET state = 'superseded' WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	store, err := Open(ctx, spec, nil)
	if err != nil {
		t.Fatalf("Open after the repair: %v", err)
	}
	_ = store.Close()
}

// assertRerunsItsDDL: every migration runs again on an initialized store, as
// after an Init or a migration interrupted past some of its statements.
func assertRerunsItsDDL(t *testing.T, store *SQLStore) {
	t.Helper()
	for v, step := range store.dialect.migrations {
		if err := store.dialect.runDDL(context.Background(), store.db, step); err != nil {
			t.Fatalf("schema version %d again: %v", v+1, err)
		}
	}
}

// assertInitCompletesPartialMeta: an Init that stopped after writing the
// schema version runs again and writes the rows it left out; on a store with
// revisions a missing row is not invented again (its value is lost).
func assertInitCompletesPartialMeta(t *testing.T, spec Spec) {
	t.Helper()
	ctx := context.Background()
	if err := Init(ctx, spec, nil); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if _, err := store.exec(ctx, store.db, "DELETE FROM smcp_store_meta WHERE meta_key <> ?",
		metaSchemaVersion); err != nil {
		t.Fatal(err)
	}
	if err := Init(ctx, spec, nil); err != nil {
		t.Fatalf("Init after a partial one: %v", err)
	}
	d := draft(t, store, "a: 1\n")
	if _, err := store.Publish(ctx, d.ID, 0, revision.Meta{Author: "root"}); err != nil {
		t.Fatalf("Publish after Init completed the metadata: %v", err)
	}
	if _, err := store.exec(ctx, store.db, "DELETE FROM smcp_store_meta WHERE meta_key = ?", metaNextID); err != nil {
		t.Fatal(err)
	}
	if err := Init(ctx, spec, nil); !errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("Init on a store with revisions missing next_id: %v, want ErrStoreCorrupt", err)
	}
}
