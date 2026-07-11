package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
)

// This file contains hand-written fakes for testing. They are exported so other
// core packages can depend on store.DB/Rows/Tx fakes without a mocking library.

// ErrScanBeforeNext is returned when Scan is called before Next.
var ErrScanBeforeNext = errors.New("store: Scan called before Next")

// ErrNoQueryFn is returned when a FakeDB has no QueryFn configured.
var ErrNoQueryFn = errors.New("store: no QueryFn configured")

// ErrNoExecFn is returned when a FakeDB has no ExecFn configured.
var ErrNoExecFn = errors.New("store: no ExecFn configured")

// ErrNoBeginFn is returned when a FakeDB has no BeginFn configured.
var ErrNoBeginFn = errors.New("store: no BeginFn configured")

// FakeRows is a hand-written Rows implementation for tests.
type FakeRows struct {
	Cols   []string
	Rows   [][]any
	err    error
	idx    int
	closed bool
}

// NewFakeRows builds a FakeRows with the given columns and rows.
func NewFakeRows(cols []string, rows ...[]any) *FakeRows {
	return &FakeRows{Cols: cols, Rows: rows}
}

// SetErr sets the terminal error returned by Err and stops iteration.
func (r *FakeRows) SetErr(err error) { r.err = err }

// Closed reports whether Close was called.
func (r *FakeRows) Closed() bool { return r.closed }

func (r *FakeRows) Next() bool {
	if r.err != nil || r.idx >= len(r.Rows) {
		return false
	}
	r.idx++
	return true
}

func (r *FakeRows) Scan(dest ...any) error {
	if r.idx == 0 {
		return ErrScanBeforeNext
	}
	row := r.Rows[r.idx-1]
	for i, v := range dest {
		rv := reflect.ValueOf(v)
		if rv.Kind() != reflect.Pointer || rv.Elem().Kind() != reflect.Interface {
			return fmt.Errorf("store: FakeRows.Scan expects *any at position %d, got %T", i, v)
		}
		ev := rv.Elem()
		if row[i] == nil {
			ev.Set(reflect.Zero(ev.Type()))
			continue
		}
		ev.Set(reflect.ValueOf(row[i]))
	}
	return nil
}

func (r *FakeRows) Columns() ([]string, error) { return r.Cols, nil }
func (r *FakeRows) Close() error               { r.closed = true; return nil }
func (r *FakeRows) Err() error                 { return r.err }

// LoggedQuery records a QueryContext call.
type LoggedQuery struct {
	Query string
	Args  []any
}

// LoggedExec records an ExecContext call.
type LoggedExec struct {
	Query string
	Args  []any
}

// FakeDB is a hand-written DB and TxBeginner for tests. Configure the *Fn
// fields to control responses; calls are recorded for assertion.
type FakeDB struct {
	QueryFn func(ctx context.Context, query string, args ...any) (Rows, error)
	ExecFn  func(ctx context.Context, query string, args ...any) (Result, error)
	BeginFn func(ctx context.Context, opts *TxOptions) (Tx, error)

	Queries []LoggedQuery
	Execs   []LoggedExec
	TxBegun int
}

func (d *FakeDB) QueryContext(ctx context.Context, query string, args ...any) (Rows, error) {
	d.Queries = append(d.Queries, LoggedQuery{Query: query, Args: args})
	if d.QueryFn == nil {
		return nil, ErrNoQueryFn
	}
	return d.QueryFn(ctx, query, args...)
}

func (d *FakeDB) ExecContext(ctx context.Context, query string, args ...any) (Result, error) {
	d.Execs = append(d.Execs, LoggedExec{Query: query, Args: args})
	if d.ExecFn == nil {
		return Result{}, ErrNoExecFn
	}
	return d.ExecFn(ctx, query, args...)
}

func (d *FakeDB) BeginTx(ctx context.Context, opts *TxOptions) (Tx, error) {
	d.TxBegun++
	if d.BeginFn == nil {
		return nil, ErrNoBeginFn
	}
	return d.BeginFn(ctx, opts)
}

// FakeTx is a hand-written Tx for tests.
type FakeTx struct {
	Committed   bool
	RolledBack  bool
	Savepoints  []string
	RollbacksTo []string
}

func (t *FakeTx) Commit() error   { t.Committed = true; return nil }
func (t *FakeTx) Rollback() error { t.RolledBack = true; return nil }

func (t *FakeTx) Savepoint(_ context.Context, name string) (Savepoint, error) {
	t.Savepoints = append(t.Savepoints, name)
	return Savepoint{Name: name}, nil
}

func (t *FakeTx) RollbackTo(_ context.Context, sp Savepoint) error {
	t.RollbacksTo = append(t.RollbacksTo, sp.Name)
	return nil
}

// QueryContext and ExecContext on FakeTx defer to package-level helpers; tests
// usually configure a shared FakeDB for tx behavior. For simplicity they return
// zero values unless overridden via fields.
func (t *FakeTx) QueryContext(_ context.Context, _ string, _ ...any) (Rows, error) {
	return nil, ErrNoQueryFn
}

func (t *FakeTx) ExecContext(_ context.Context, _ string, _ ...any) (Result, error) {
	return Result{}, ErrNoExecFn
}
