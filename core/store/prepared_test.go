package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// preparedFakeDB prepares statements and counts them; it runs nothing
// unprepared.
type preparedFakeDB struct {
	prepared int
	closed   int
	mu       sync.Mutex
}

func (d *preparedFakeDB) QueryContext(context.Context, string, ...any) (Rows, error) {
	return nil, errors.New("unprepared query")
}

func (d *preparedFakeDB) ExecContext(context.Context, string, ...any) (Result, error) {
	return Result{}, errors.New("unprepared exec")
}

// closedRows is a Rows with no rows, closed when the test is done with it.
type closedRows struct{}

func (closedRows) Next() bool                 { return false }
func (closedRows) Scan(...any) error          { return errors.New("no rows") }
func (closedRows) Columns() ([]string, error) { return []string{"id"}, nil }
func (closedRows) Close() error               { return nil }
func (closedRows) Err() error                 { return nil }

func (d *preparedFakeDB) PrepareContext(context.Context, string) (Prepared, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.prepared++
	return &preparedFakeStmt{db: d}, nil
}

type preparedFakeStmt struct{ db *preparedFakeDB }

func (s *preparedFakeStmt) QueryContext(context.Context, ...any) (Rows, error) {
	return closedRows{}, nil
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

// staleStmtDB prepares statements whose plan goes stale for good once its
// table changed: only one prepared afresh runs.
type staleStmtDB struct {
	preparedFakeDB
	changed bool
}

func (d *staleStmtDB) PrepareContext(context.Context, string) (Prepared, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.prepared++
	return &staleStmt{preparedFakeStmt: preparedFakeStmt{db: &d.preparedFakeDB}, db: d, changed: d.changed}, nil
}

type staleStmt struct {
	preparedFakeStmt
	db      *staleStmtDB
	changed bool // prepared after the change
}

func (s *staleStmt) QueryContext(context.Context, ...any) (Rows, error) {
	if s.db.changed && !s.changed {
		return nil, ErrStalePlan
	}
	return closedRows{}, nil
}

// A statement refused for a stale plan is prepared again when next run,
// as the database keeps refusing the one prepared before.
func TestPreparedDBPreparesAStalePlanAgain(t *testing.T) {
	db := &staleStmtDB{}
	cached := WithPreparedCache(db, 4)
	query := func() error {
		rows, err := cached.QueryContext(context.Background(), "select 1")
		if err == nil {
			_ = rows.Close()
		}
		return err
	}
	if err := query(); err != nil {
		t.Fatal(err)
	}
	db.changed = true
	if err := query(); !errors.Is(err, ErrStalePlan) {
		t.Fatalf("first run after the change = %v", err)
	}
	if err := query(); err != nil || db.prepared != 2 || db.closed != 1 {
		t.Fatalf("run again = %v, prepared %d, closed %d", err, db.prepared, db.closed)
	}
}

// The statement used least recently is the one evicted.
func TestPreparedDBEvictsTheLeastRecentlyUsed(t *testing.T) {
	db := &preparedFakeDB{}
	cached := WithPreparedCache(db, 2)
	for _, q := range []string{"a", "b", "a", "c", "a"} {
		if _, err := cached.ExecContext(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	if db.prepared != 3 {
		t.Fatalf("prepared %d, want a kept while used", db.prepared)
	}
}

// ctxPreparedDB prepares as its context allows: the first prepare waits for
// release, then fails if its context ended.
type ctxPreparedDB struct {
	preparedFakeDB
	started chan struct{}
	release chan struct{}
	first   sync.Once
}

func (d *ctxPreparedDB) PrepareContext(ctx context.Context, _ string) (Prepared, error) {
	d.first.Do(func() {
		close(d.started)
		<-d.release
	})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.prepared++
	d.mu.Unlock()
	return &preparedFakeStmt{db: &d.preparedFakeDB}, nil
}

// A statement waiting on another call's prepare is not failed by that
// call's request ending: it is prepared for the one waiting.
func TestPreparedDBWaiterOutlivesItsPreparer(t *testing.T) {
	db := &ctxPreparedDB{started: make(chan struct{}), release: make(chan struct{})}
	cached := WithPreparedCache(db, 4)
	leaderCtx, cancel := context.WithCancel(context.Background())
	leader := make(chan error, 1)
	go func() {
		_, err := cached.ExecContext(leaderCtx, "update t set x = 1")
		leader <- err
	}()
	<-db.started
	waiter := make(chan error, 1)
	go func() {
		_, err := cached.ExecContext(context.Background(), "update t set x = 1")
		waiter <- err
	}()
	time.Sleep(20 * time.Millisecond) // the second waits for the first prepare
	cancel()
	close(db.release)
	if err := <-leader; !errors.Is(err, context.Canceled) {
		t.Fatalf("the canceled call = %v", err)
	}
	if err := <-waiter; err != nil {
		t.Fatalf("the waiting call failed with its preparer's end: %v", err)
	}
}
