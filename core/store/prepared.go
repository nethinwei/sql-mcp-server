package store

import (
	"context"
	"sync"
)

// Prepared is a reusable statement.
type Prepared interface {
	QueryContext(ctx context.Context, args ...any) (Rows, error)
	ExecContext(ctx context.Context, args ...any) (Result, error)
	Close() error
}

// Preparer is an optional DB capability. Existing DB fakes need not implement it.
type Preparer interface {
	PrepareContext(ctx context.Context, query string) (Prepared, error)
}

// PreparedDB uses an optional bounded prepared-statement cache while preserving
// the DB interface. A non-Preparer DB is passed through unchanged.
type PreparedDB struct {
	DB
	max        int
	mu         sync.Mutex
	items      map[string]*preparedEntry
	order      []string
	preparing  map[string]*prepareCall
	generation uint64
}

type preparedEntry struct {
	stmt    Prepared
	refs    int
	retired bool
}

type prepareCall struct {
	done       chan struct{}
	entry      *preparedEntry
	err        error
	waiters    int
	generation uint64
	complete   bool
}

// WithPreparedCache wraps db. max <= 0 disables preparing.
func WithPreparedCache(db DB, max int) *PreparedDB {
	return &PreparedDB{
		DB:        db,
		max:       max,
		items:     map[string]*preparedEntry{},
		preparing: map[string]*prepareCall{},
	}
}

func (d *PreparedDB) statement(ctx context.Context, query string) (*preparedEntry, bool, error) {
	preparer, ok := d.DB.(Preparer)
	if !ok || d.max <= 0 {
		return nil, false, nil
	}
	d.mu.Lock()
	if entry, ok := d.items[query]; ok {
		entry.refs++
		d.mu.Unlock()
		return entry, true, nil
	}
	if call, ok := d.preparing[query]; ok {
		call.waiters++
		d.mu.Unlock()
		return d.waitPrepare(ctx, call)
	}
	call := &prepareCall{
		done:       make(chan struct{}),
		waiters:    1,
		generation: d.generation,
	}
	d.preparing[query] = call
	d.mu.Unlock()

	stmt, err := preparer.PrepareContext(ctx, query)
	d.mu.Lock()
	defer d.mu.Unlock()
	if err != nil {
		return d.finishPrepareErrorLocked(query, call, err)
	}
	return d.finishPrepareSuccessLocked(query, call, stmt)
}

func (d *PreparedDB) finishPrepareErrorLocked(
	query string,
	call *prepareCall,
	err error,
) (*preparedEntry, bool, error) {
	call.err = err
	call.complete = true
	delete(d.preparing, query)
	close(call.done)
	return nil, false, err
}

func (d *PreparedDB) finishPrepareSuccessLocked(
	query string,
	call *prepareCall,
	stmt Prepared,
) (*preparedEntry, bool, error) {
	entry := &preparedEntry{stmt: stmt, refs: call.waiters}
	call.entry = entry
	call.complete = true
	if call.generation == d.generation {
		if len(d.order) >= d.max {
			oldest := d.order[0]
			d.order = d.order[1:]
			oldEntry := d.items[oldest]
			delete(d.items, oldest)
			d.retireLocked(oldEntry)
		}
		d.items[query] = entry
		d.order = append(d.order, query)
	} else {
		entry.retired = true
	}
	delete(d.preparing, query)
	close(call.done)
	return entry, true, nil
}

func (d *PreparedDB) waitPrepare(ctx context.Context, call *prepareCall) (*preparedEntry, bool, error) {
	select {
	case <-call.done:
		return call.entry, call.err == nil, call.err
	case <-ctx.Done():
		d.mu.Lock()
		call.waiters--
		if call.complete && call.entry != nil {
			d.releaseLocked(call.entry)
		}
		d.mu.Unlock()
		return nil, false, ctx.Err()
	}
}

func (d *PreparedDB) retireLocked(entry *preparedEntry) {
	entry.retired = true
	if entry.refs == 0 {
		_ = entry.stmt.Close()
	}
}

func (d *PreparedDB) release(entry *preparedEntry) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.releaseLocked(entry)
}

func (d *PreparedDB) releaseLocked(entry *preparedEntry) {
	entry.refs--
	if entry.refs == 0 && entry.retired {
		_ = entry.stmt.Close()
	}
}

type leasedRows struct {
	Rows
	once    sync.Once
	release func()
}

func (r *leasedRows) Close() error {
	err := r.Rows.Close()
	r.once.Do(r.release)
	return err
}

// QueryContext implements DB.
func (d *PreparedDB) QueryContext(ctx context.Context, query string, args ...any) (Rows, error) {
	entry, ok, err := d.statement(ctx, query)
	if err != nil {
		return nil, err
	}
	if ok {
		rows, err := entry.stmt.QueryContext(ctx, args...)
		if err != nil {
			d.release(entry)
			return nil, err
		}
		return &leasedRows{Rows: rows, release: func() { d.release(entry) }}, nil
	}
	return d.DB.QueryContext(ctx, query, args...)
}

// ExecContext implements DB.
func (d *PreparedDB) ExecContext(ctx context.Context, query string, args ...any) (Result, error) {
	entry, ok, err := d.statement(ctx, query)
	if err != nil {
		return Result{}, err
	}
	if ok {
		defer d.release(entry)
		return entry.stmt.ExecContext(ctx, args...)
	}
	return d.DB.ExecContext(ctx, query, args...)
}

// Close clears all prepared statements. It does not close the wrapped DB.
func (d *PreparedDB) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.generation++
	var first error
	for _, entry := range d.items {
		entry.retired = true
		if entry.refs == 0 {
			if err := entry.stmt.Close(); err != nil && first == nil {
				first = err
			}
		}
	}
	d.items = map[string]*preparedEntry{}
	d.order = nil
	return first
}
