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
	max   int
	mu    sync.Mutex
	items map[string]*preparedEntry
	order []string
}

type preparedEntry struct {
	stmt    Prepared
	refs    int
	retired bool
}

// WithPreparedCache wraps db. max <= 0 disables preparing.
func WithPreparedCache(db DB, max int) *PreparedDB {
	return &PreparedDB{DB: db, max: max, items: map[string]*preparedEntry{}}
}

func (d *PreparedDB) statement(ctx context.Context, query string) (*preparedEntry, bool, error) {
	preparer, ok := d.DB.(Preparer)
	if !ok || d.max <= 0 {
		return nil, false, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if entry, ok := d.items[query]; ok {
		entry.refs++
		return entry, true, nil
	}
	stmt, err := preparer.PrepareContext(ctx, query)
	if err != nil {
		return nil, false, err
	}
	if len(d.order) >= d.max {
		oldest := d.order[0]
		d.order = d.order[1:]
		entry := d.items[oldest]
		delete(d.items, oldest)
		d.retireLocked(entry)
	}
	entry := &preparedEntry{stmt: stmt, refs: 1}
	d.items[query] = entry
	d.order = append(d.order, query)
	return entry, true, nil
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
