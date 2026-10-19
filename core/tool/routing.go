package tool

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/store"
)

// connectionFor returns the connection of source an action uses: Write for
// creates, updates and deletes, Execute for procedures, otherwise DB (the read
// connection), switched to Write while the session's own write is recent.
func connectionFor(tc Context, source DataSource, action entity.Action) store.DB {
	switch action {
	case entity.ActionCreate, entity.ActionUpdate, entity.ActionDelete:
		return orDB(source.Write, source.DB)
	case entity.ActionExecute:
		return orDB(source.Execute, source.DB)
	}
	if followsWrite(tc, source, tc.DataSource) {
		return orDB(source.Write, source.DB)
	}
	return source.DB
}

// followsWrite reports whether the session's reads of datasource follow its
// recent write there to the write connection. Such reads bypass the shared
// read cache and singleflight, which other sessions fill from the read
// connection (a replica may not have the write yet).
func followsWrite(tc Context, source DataSource, datasource string) bool {
	return source.ReadAfterWrite > 0 && tc.Writes.Recent(tc.Session, datasource)
}

func orDB(db, fallback store.DB) store.DB {
	if db != nil {
		return db
	}
	return fallback
}

// routeEntity points tc at the datasource of e and the connection action
// uses; inside an explicit transaction every statement uses its connection.
func routeEntity(ctx context.Context, tc Context, e entity.Entity, action entity.Action) (Context, error) {
	name := e.DatasourceName()
	tc.DataSource = name
	if len(tc.Sources) == 0 {
		return tc, nil
	}
	source, ok := tc.Sources[name]
	if !ok {
		return tc, fmt.Errorf("%w: datasource %q", ErrEntityNotFound, name)
	}
	tc.Dialect, tc.Gate, tc.Analyze = source.Dialect, source.Gate, source.Analyze
	tc.DB = connectionFor(tc, source, action)
	tc.followsWrite = followsWrite(tc, source, name)
	if tc.Transaction != "" {
		if tc.Transactions == nil {
			return tc, ErrTransactionNotFound
		}
		db, err := tc.Transactions.DB(ctx, tc.Transaction, callerOf(tc), name, txConnections(tc, name))
		if err != nil {
			return tc, err
		}
		tc.DB = db
	}
	return tc, nil
}

// WriteTracker remembers until when each session's reads of each datasource
// follow its last write there (the datasource's readAfterWrite window). A
// nil tracker records nothing.
type WriteTracker struct {
	mu        sync.Mutex
	until     map[string]map[string]time.Time
	now       func() time.Time
	lastPrune time.Time
}

// maxTrackedWindow bounds how long a write is remembered; pruneInterval is
// how often expired entries of every session are swept.
const (
	maxTrackedWindow = time.Hour
	pruneInterval    = time.Minute
)

// NewWriteTracker returns an empty tracker.
func NewWriteTracker() *WriteTracker {
	return &WriteTracker{until: map[string]map[string]time.Time{}, now: time.Now}
}

// Record notes a write by session to datasource, whose reads follow it for
// window (at most maxTrackedWindow). A datasource without a window (its
// reads never follow writes) records nothing.
func (w *WriteTracker) Record(session, datasource string, window time.Duration) {
	if w == nil || window <= 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	byDatasource := w.until[session]
	if byDatasource == nil {
		byDatasource = map[string]time.Time{}
		w.until[session] = byDatasource
	}
	expire(byDatasource, now)
	byDatasource[datasource] = now.Add(min(window, maxTrackedWindow))
	if now.Sub(w.lastPrune) >= pruneInterval {
		w.lastPrune = now
		for s, d := range w.until {
			if expire(d, now) == 0 {
				delete(w.until, s)
			}
		}
	}
}

// Recent reports whether session's reads of datasource follow its write.
func (w *WriteTracker) Recent(session, datasource string) bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	until, ok := w.until[session][datasource]
	return ok && w.now().Before(until)
}

// Wrote reports whether any of session's reads follow its writes now.
func (w *WriteTracker) Wrote(session string) bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	for _, until := range w.until[session] {
		if now.Before(until) {
			return true
		}
	}
	return false
}

// Forget drops a closed session.
func (w *WriteTracker) Forget(session string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.until, session)
}

// expire drops the windows of byDatasource that ended and returns how many
// remain.
func expire(byDatasource map[string]time.Time, now time.Time) int {
	for datasource, until := range byDatasource {
		if !now.Before(until) {
			delete(byDatasource, datasource)
		}
	}
	return len(byDatasource)
}
