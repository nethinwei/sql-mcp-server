// Package revisiontest provides the conformance suite every revision.Store
// implementation must pass, so SQL stores keep the MemoryStore semantics.
package revisiontest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/revision"
)

// NewStore returns an empty store; the suite closes it.
type NewStore func(t *testing.T) revision.Store

// Run runs the conformance suite against stores produced by newStore.
func Run(t *testing.T, newStore NewStore) {
	t.Helper()
	cases := map[string]func(*testing.T, revision.Store){
		"CreateGetList":         testCreateGetList,
		"PublishLifecycle":      testPublishLifecycle,
		"PublishConflict":       testPublishConflict,
		"RollbackSkipsSameHash": testRollbackSkipsSameHash,
		"RollbackExplicit":      testRollbackExplicit,
		"ImportHistory":         testImportHistory,
		"ConcurrentPublish":     testConcurrentPublish,
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			store := newStore(t)
			t.Cleanup(func() { _ = store.Close() })
			fn(t, store)
		})
	}
}

func mustCreate(t *testing.T, s revision.Store, payload string) revision.Revision {
	t.Helper()
	r, err := s.Create(context.Background(), revision.Draft{Payload: []byte(payload), Author: "tester", Comment: payload})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func mustPublish(t *testing.T, s revision.Store, id, expected int64) revision.Revision {
	t.Helper()
	r, err := s.Publish(context.Background(), id, expected, revision.Meta{Author: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func states(t *testing.T, s revision.Store) map[int64]revision.State {
	t.Helper()
	list, err := s.List(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[int64]revision.State, len(list))
	for _, r := range list {
		out[r.ID] = r.State
	}
	return out
}

func testCreateGetList(t *testing.T, s revision.Store) {
	ctx := context.Background()
	a := mustCreate(t, s, "a: 1\n")
	b, err := s.Create(ctx, revision.Draft{ParentID: a.ID, Payload: []byte("b: 2\n"), Author: "bob", Comment: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID <= 0 || b.ID <= a.ID || a.State != revision.StateDraft || a.ContentHash != revision.Hash([]byte("a: 1\n")) {
		t.Fatalf("created revisions = %+v %+v", a, b)
	}
	got, err := s.Get(ctx, b.ID)
	if err != nil || string(got.Payload) != "b: 2\n" || got.ParentID != a.ID || got.Author != "bob" || got.Comment != "c" {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if err := got.Verify(); err != nil {
		t.Fatal(err)
	}
	if got.CreatedAt.IsZero() || !got.PublishedAt.IsZero() {
		t.Fatalf("timestamps = %v %v", got.CreatedAt, got.PublishedAt)
	}
	if _, err := s.Get(ctx, 999); !errors.Is(err, revision.ErrNotFound) {
		t.Fatalf("missing revision err = %v", err)
	}
	list, err := s.List(ctx, 1)
	if err != nil || len(list) != 1 || list[0].ID != b.ID || list[0].Payload != nil {
		t.Fatalf("List(1) = %+v, %v", list, err)
	}
	if _, err := s.Published(ctx); !errors.Is(err, revision.ErrNoPublished) {
		t.Fatalf("Published on fresh store err = %v", err)
	}
}

func testPublishLifecycle(t *testing.T, s revision.Store) {
	ctx := context.Background()
	a := mustCreate(t, s, "a\n")
	b := mustCreate(t, s, "b\n")
	pa := mustPublish(t, s, a.ID, 0)
	if pa.State != revision.StatePublished || pa.PublishedAt.IsZero() {
		t.Fatalf("published = %+v", pa)
	}
	mustPublish(t, s, b.ID, a.ID)
	if st := states(t, s); st[a.ID] != revision.StateSuperseded || st[b.ID] != revision.StatePublished {
		t.Fatalf("states = %v", st)
	}
	cur, err := s.Published(ctx)
	if err != nil || cur.ID != b.ID || string(cur.Payload) != "b\n" {
		t.Fatalf("Published = %+v, %v", cur, err)
	}
	if _, err := s.Publish(ctx, a.ID, b.ID, revision.Meta{}); !errors.Is(err, revision.ErrInvalidState) {
		t.Fatalf("republishing a superseded revision err = %v", err)
	}
	if _, err := s.Publish(ctx, 999, b.ID, revision.Meta{}); !errors.Is(err, revision.ErrNotFound) {
		t.Fatalf("publishing a missing revision err = %v", err)
	}
}

func testPublishConflict(t *testing.T, s revision.Store) {
	ctx := context.Background()
	a := mustCreate(t, s, "a\n")
	b := mustCreate(t, s, "b\n")
	if _, err := s.Publish(ctx, a.ID, 42, revision.Meta{}); !errors.Is(err, revision.ErrConflict) {
		t.Fatalf("stale expectation with nothing published err = %v", err)
	}
	mustPublish(t, s, a.ID, 0)
	if _, err := s.Publish(ctx, b.ID, 0, revision.Meta{}); !errors.Is(err, revision.ErrConflict) {
		t.Fatalf("stale expectation err = %v", err)
	}
	if st := states(t, s); st[a.ID] != revision.StatePublished || st[b.ID] != revision.StateDraft {
		t.Fatalf("conflict must not change states: %v", st)
	}
}

func testRollbackSkipsSameHash(t *testing.T, s revision.Store) {
	ctx := context.Background()
	if _, err := s.Rollback(ctx, 0, 0, revision.Meta{}); !errors.Is(err, revision.ErrNoPublished) {
		t.Fatalf("rollback with nothing published err = %v", err)
	}
	a := mustCreate(t, s, "a\n")
	b := mustCreate(t, s, "b\n")
	c := mustCreate(t, s, "c\n")
	mustPublish(t, s, a.ID, 0)
	if _, err := s.Rollback(ctx, a.ID, 0, revision.Meta{}); !errors.Is(err, revision.ErrNoRollback) {
		t.Fatalf("rollback without history err = %v", err)
	}
	mustPublish(t, s, b.ID, a.ID)
	mustPublish(t, s, c.ID, b.ID)
	first, err := s.Rollback(ctx, c.ID, 0, revision.Meta{Author: "ops", Comment: "undo c"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID <= c.ID || first.ParentID != b.ID || string(first.Payload) != "b\n" ||
		first.State != revision.StatePublished || first.Author != "ops" {
		t.Fatalf("first rollback = %+v", first)
	}
	if st := states(t, s); st[c.ID] != revision.StateRolledBack {
		t.Fatalf("rolled-back state = %v", st)
	}
	second, err := s.Rollback(ctx, first.ID, 0, revision.Meta{})
	if err != nil {
		t.Fatal(err)
	}
	if second.ParentID != a.ID || string(second.Payload) != "a\n" {
		t.Fatalf("second rollback must skip the identical content of b: %+v", second)
	}
	if _, err := s.Rollback(ctx, first.ID, 0, revision.Meta{}); !errors.Is(err, revision.ErrConflict) {
		t.Fatalf("stale rollback err = %v", err)
	}
}

func testRollbackExplicit(t *testing.T, s revision.Store) {
	ctx := context.Background()
	a := mustCreate(t, s, "a\n")
	b := mustCreate(t, s, "b\n")
	draft := mustCreate(t, s, "draft\n")
	mustPublish(t, s, a.ID, 0)
	mustPublish(t, s, b.ID, a.ID)
	if _, err := s.Rollback(ctx, b.ID, draft.ID, revision.Meta{}); !errors.Is(err, revision.ErrInvalidState) {
		t.Fatalf("rollback to a draft err = %v", err)
	}
	if _, err := s.Rollback(ctx, b.ID, 999, revision.Meta{}); !errors.Is(err, revision.ErrNotFound) {
		t.Fatalf("rollback to a missing revision err = %v", err)
	}
	r, err := s.Rollback(ctx, b.ID, a.ID, revision.Meta{})
	if err != nil || r.ParentID != a.ID || string(r.Payload) != "a\n" {
		t.Fatalf("explicit rollback = %+v, %v", r, err)
	}
}

func testImportHistory(t *testing.T, s revision.Store) {
	ctx := context.Background()
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	history := []revision.Revision{
		{ID: 3, ContentHash: revision.Hash([]byte("x\n")), Payload: []byte("x\n"), State: revision.StateSuperseded,
			Author: "a", CreatedAt: at, PublishedAt: at},
		{ID: 7, ParentID: 3, ContentHash: revision.Hash([]byte("y\n")), Payload: []byte("y\n"),
			State: revision.StatePublished, Author: "b", Comment: "c", CreatedAt: at, PublishedAt: at.Add(time.Hour)},
	}
	bad := append([]revision.Revision(nil), history...)
	bad[0].ContentHash = revision.Hash([]byte("tampered"))
	if err := s.ImportHistory(ctx, bad); !errors.Is(err, revision.ErrHashMismatch) {
		t.Fatalf("tampered history err = %v", err)
	}
	if err := s.ImportHistory(ctx, history); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, 7)
	if err != nil || got.ParentID != 3 || got.Comment != "c" || !got.CreatedAt.Equal(at) ||
		!got.PublishedAt.Equal(at.Add(time.Hour)) || got.State != revision.StatePublished {
		t.Fatalf("imported revision = %+v, %v", got, err)
	}
	if next := mustCreate(t, s, "z\n"); next.ID <= 7 {
		t.Fatalf("IDs must continue after imported history: %d", next.ID)
	}
	if err := s.ImportHistory(ctx, history); !errors.Is(err, revision.ErrNotEmpty) {
		t.Fatalf("import into a non-empty store err = %v", err)
	}
}

// testConcurrentPublish races publishes that all expect the same current
// revision: exactly one may win, and exactly one revision ends up published.
func testConcurrentPublish(t *testing.T, s revision.Store) {
	ctx := context.Background()
	base := mustCreate(t, s, "base\n")
	mustPublish(t, s, base.ID, 0)
	const n = 8
	drafts := make([]revision.Revision, n)
	for i := range drafts {
		drafts[i] = mustCreate(t, s, string(rune('a'+i))+"\n")
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for _, d := range drafts {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			_, err := s.Publish(ctx, id, base.ID, revision.Meta{})
			if err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			} else if !errors.Is(err, revision.ErrConflict) {
				t.Errorf("concurrent publish err = %v", err)
			}
		}(d.ID)
	}
	wg.Wait()
	published := 0
	for _, st := range states(t, s) {
		if st == revision.StatePublished {
			published++
		}
	}
	if wins != 1 || published != 1 {
		t.Fatalf("wins = %d, published = %d, want 1 and 1", wins, published)
	}
}
