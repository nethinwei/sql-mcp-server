package tool

import (
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
	return source.ReadAfterWrite > 0 && tc.Writes.Recent(tc.Session, datasource, source.ReadAfterWrite)
}

func orDB(db, fallback store.DB) store.DB {
	if db != nil {
		return db
	}
	return fallback
}

// routeEntity points tc at the datasource of e and the connection action
// uses; inside an explicit transaction every statement uses its connection.
func routeEntity(tc Context, e entity.Entity, action entity.Action) (Context, error) {
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
		db, err := tc.Transactions.DB(tc.Transaction, tc.Session, tc.Role, tc.Subject, name)
		if err != nil {
			return tc, err
		}
		tc.DB = db
	}
	return tc, nil
}

// WriteTracker remembers when each session last wrote to each datasource, so
// its reads can follow its writes for a datasource's readAfterWrite window. A
// nil tracker records nothing.
type WriteTracker struct {
	mu   sync.Mutex
	last map[string]map[string]time.Time
	now  func() time.Time
}

// maxTrackedWindow bounds how long a write is remembered.
const maxTrackedWindow = time.Hour

// NewWriteTracker returns an empty tracker.
func NewWriteTracker() *WriteTracker {
	return &WriteTracker{last: map[string]map[string]time.Time{}, now: time.Now}
}

// Record notes a write by session to datasource.
func (w *WriteTracker) Record(session, datasource string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	byDatasource := w.last[session]
	if byDatasource == nil {
		byDatasource = map[string]time.Time{}
		w.last[session] = byDatasource
	}
	byDatasource[datasource] = now
	if len(w.last) > 4096 {
		w.pruneLocked(now)
	}
}

// Recent reports whether session wrote to datasource within window.
func (w *WriteTracker) Recent(session, datasource string, window time.Duration) bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	at, ok := w.last[session][datasource]
	return ok && w.now().Sub(at) < window
}

// Wrote reports whether session wrote anything recently enough to be
// remembered.
func (w *WriteTracker) Wrote(session string) bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.last[session]) > 0
}

// Forget drops a closed session.
func (w *WriteTracker) Forget(session string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.last, session)
}

func (w *WriteTracker) pruneLocked(now time.Time) {
	for session, byDatasource := range w.last {
		for datasource, at := range byDatasource {
			if now.Sub(at) >= maxTrackedWindow {
				delete(byDatasource, datasource)
			}
		}
		if len(byDatasource) == 0 {
			delete(w.last, session)
		}
	}
}
