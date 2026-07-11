package store

import (
	"context"
	"sync"
	"testing"
	"time"
)

type preparedFakeDB struct {
	FakeDB
	prepared int
	closed   int
	mu       sync.Mutex
}

func (d *preparedFakeDB) PrepareContext(context.Context, string) (Prepared, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.prepared++
	return &preparedFakeStmt{db: d}, nil
}

type preparedFakeStmt struct{ db *preparedFakeDB }

func (s *preparedFakeStmt) QueryContext(context.Context, ...any) (Rows, error) {
	return NewFakeRows([]string{"id"}, []any{1}), nil
}
func (s *preparedFakeStmt) ExecContext(context.Context, ...any) (Result, error) {
	return Result{RowsAffected: 1}, nil
}
func (s *preparedFakeStmt) Close() error {
	s.db.mu.Lock()
	defer s.db.mu.Unlock()
	s.db.closed++
	return nil
}

type blockingPreparedDB struct {
	preparedFakeDB
	started chan string
	release chan struct{}
}

func (d *blockingPreparedDB) PrepareContext(_ context.Context, query string) (Prepared, error) {
	d.mu.Lock()
	d.prepared++
	d.mu.Unlock()
	d.started <- query
	<-d.release
	return &preparedFakeStmt{db: &d.preparedFakeDB}, nil
}

func TestPreparedDBCachesAndEvicts(t *testing.T) {
	db := &preparedFakeDB{}
	cached := WithPreparedCache(db, 1)
	rows, err := cached.QueryContext(context.Background(), "select 1")
	if err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	rows, err = cached.QueryContext(context.Background(), "select 1")
	if err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	if db.prepared != 1 {
		t.Fatalf("prepared = %d, want 1", db.prepared)
	}
	if _, err := cached.ExecContext(context.Background(), "update t set x=1"); err != nil {
		t.Fatal(err)
	}
	if db.closed != 1 {
		t.Fatalf("closed = %d, want eviction close", db.closed)
	}
	if err := cached.Close(); err != nil {
		t.Fatal(err)
	}
	if db.closed != 2 {
		t.Fatalf("closed = %d, want all statements closed", db.closed)
	}
}

func TestPreparedDBEvictionWaitsForRowsClose(t *testing.T) {
	db := &preparedFakeDB{}
	cached := WithPreparedCache(db, 1)
	rows, err := cached.QueryContext(context.Background(), "select 1")
	if err != nil {
		t.Fatal(err)
	}
	evicted := make(chan error, 1)
	go func() {
		_, err := cached.ExecContext(context.Background(), "update t set x=1")
		evicted <- err
	}()
	if err := <-evicted; err != nil {
		t.Fatal(err)
	}
	db.mu.Lock()
	closed := db.closed
	db.mu.Unlock()
	if closed != 0 {
		t.Fatalf("in-use statement closed during eviction: %d", closed)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	db.mu.Lock()
	closed = db.closed
	db.mu.Unlock()
	if closed != 1 {
		t.Fatalf("closed after rows release = %d, want 1", closed)
	}
}

func TestPreparedDBCloseWaitsForRowsClose(t *testing.T) {
	db := &preparedFakeDB{}
	cached := WithPreparedCache(db, 1)
	rows, err := cached.QueryContext(context.Background(), "select 1")
	if err != nil {
		t.Fatal(err)
	}
	if err := cached.Close(); err != nil {
		t.Fatal(err)
	}
	db.mu.Lock()
	closed := db.closed
	db.mu.Unlock()
	if closed != 0 {
		t.Fatalf("in-use statement closed by cache close: %d", closed)
	}
	_ = rows.Close()
	db.mu.Lock()
	closed = db.closed
	db.mu.Unlock()
	if closed != 1 {
		t.Fatalf("closed after rows release = %d, want 1", closed)
	}
}

func TestPreparedDBFallsBackForPlainDB(t *testing.T) {
	db := &FakeDB{ExecFn: func(context.Context, string, ...any) (Result, error) {
		return Result{RowsAffected: 1}, nil
	}}
	if _, err := WithPreparedCache(db, 2).ExecContext(context.Background(), "update t set x=1"); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedDBPreparesDifferentQueriesConcurrently(t *testing.T) {
	db := &blockingPreparedDB{
		started: make(chan string, 2),
		release: make(chan struct{}),
	}
	cached := WithPreparedCache(db, 2)
	errs := make(chan error, 2)
	for _, query := range []string{"select 1", "select 2"} {
		go func() {
			_, err := cached.ExecContext(context.Background(), query)
			errs <- err
		}()
	}
	seen := map[string]bool{}
	for range 2 {
		select {
		case query := <-db.started:
			seen[query] = true
		case <-time.After(time.Second):
			t.Fatal("PrepareContext calls were serialized")
		}
	}
	close(db.release)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if !seen["select 1"] || !seen["select 2"] {
		t.Fatalf("prepared queries = %v", seen)
	}
}

func TestPreparedDBConcurrentSameQueryPreparesOnce(t *testing.T) {
	const callers = 16
	db := &blockingPreparedDB{
		started: make(chan string, 1),
		release: make(chan struct{}),
	}
	cached := WithPreparedCache(db, 2)
	errs := make(chan error, callers)
	for range callers {
		go func() {
			_, err := cached.ExecContext(context.Background(), "select 1")
			errs <- err
		}()
	}
	<-db.started
	deadline := time.Now().Add(time.Second)
	for {
		cached.mu.Lock()
		call := cached.preparing["select 1"]
		joined := call != nil && call.waiters == callers
		cached.mu.Unlock()
		if joined {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("concurrent callers did not join preparation")
		}
		time.Sleep(time.Millisecond)
	}
	close(db.release)
	for range callers {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	db.mu.Lock()
	prepared := db.prepared
	db.mu.Unlock()
	if prepared != 1 {
		t.Fatalf("prepared = %d, want 1", prepared)
	}
}

func TestPreparedDBCloseDuringPrepareRetiresStatement(t *testing.T) {
	db := &blockingPreparedDB{
		started: make(chan string, 1),
		release: make(chan struct{}),
	}
	cached := WithPreparedCache(db, 1)
	done := make(chan error, 1)
	go func() {
		_, err := cached.ExecContext(context.Background(), "select 1")
		done <- err
	}()
	<-db.started
	if err := cached.Close(); err != nil {
		t.Fatal(err)
	}
	close(db.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	db.mu.Lock()
	closed := db.closed
	db.mu.Unlock()
	if closed != 1 {
		t.Fatalf("closed = %d, want prepared statement retired", closed)
	}
}

func BenchmarkPreparedDBParallelCacheHit(b *testing.B) {
	cached := WithPreparedCache(&preparedFakeDB{}, 1)
	if _, err := cached.ExecContext(context.Background(), "select 1"); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := cached.ExecContext(context.Background(), "select 1"); err != nil {
				b.Fatal(err)
			}
		}
	})
}
