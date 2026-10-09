// Package sqladapter adapts a database/sql pool to the core store interfaces.
// It is shared by the providers built on database/sql drivers.
package sqladapter

import (
	"context"
	"database/sql"

	"github.com/nethinwei/sql-mcp-server/core/store"
)

// RowsFunc wraps driver rows, e.g. to normalize scanned values.
type RowsFunc func(*sql.Rows) store.Rows

// ClassifyFunc turns a driver error into a store error the gateway acts on
// (such as *store.ConstraintError), or returns it unchanged.
type ClassifyFunc func(error) error

// Pool wraps *sql.DB, satisfying store.DB, store.Preparer and store.TxBeginner.
// Embedders get DB(), which bootstrap uses to size the pool and ping it; the
// type is not named DB so an embedded field cannot shadow that method.
type Pool struct {
	db       *sql.DB
	rows     RowsFunc
	classify ClassifyFunc
}

// New wraps db. A nil rows returns the driver rows unchanged; a nil classify
// leaves errors unchanged. Statement errors and errors met while reading rows
// (where PostgreSQL reports a failed INSERT ... RETURNING) are classified.
func New(db *sql.DB, rows RowsFunc, classify ClassifyFunc) *Pool {
	if rows == nil {
		rows = func(r *sql.Rows) store.Rows { return r }
	}
	if classify == nil {
		classify = func(err error) error { return err }
	}
	return &Pool{db: db, rows: rows, classify: classify}
}

// DB returns the underlying pool, for pool configuration and native pings.
func (d *Pool) DB() *sql.DB { return d.db }

// Close closes the pool.
func (d *Pool) Close() error { return d.db.Close() }

// QueryContext implements store.DB.
func (d *Pool) QueryContext(ctx context.Context, query string, args ...any) (store.Rows, error) {
	return d.query(d.db.QueryContext(ctx, query, args...))
}

// ExecContext implements store.DB.
func (d *Pool) ExecContext(ctx context.Context, query string, args ...any) (store.Result, error) {
	return d.result(d.db.ExecContext(ctx, query, args...))
}

// PrepareContext implements store.Preparer.
func (d *Pool) PrepareContext(ctx context.Context, query string) (store.Prepared, error) {
	s, err := d.db.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return stmt{Stmt: s, db: d}, nil
}

// BeginTx implements store.TxBeginner.
func (d *Pool) BeginTx(ctx context.Context, opts *store.TxOptions) (store.Tx, error) {
	var sqlOpts *sql.TxOptions
	if opts != nil {
		sqlOpts = &sql.TxOptions{Isolation: isolation(opts.Isolation), ReadOnly: opts.ReadOnly}
	}
	t, err := d.db.BeginTx(ctx, sqlOpts)
	if err != nil {
		return nil, err
	}
	return tx{Tx: t, db: d}, nil
}

func (d *Pool) query(rows *sql.Rows, err error) (store.Rows, error) {
	if err != nil {
		return nil, d.classify(err)
	}
	return classifiedRows{Rows: d.rows(rows), classify: d.classify}, nil
}

func (d *Pool) result(res sql.Result, err error) (store.Result, error) {
	if err != nil {
		return store.Result{}, d.classify(err)
	}
	li, _ := res.LastInsertId()
	ra, _ := res.RowsAffected()
	return store.Result{LastInsertID: li, RowsAffected: ra}, nil
}

// classifiedRows classifies the errors met while reading rows.
type classifiedRows struct {
	store.Rows
	classify ClassifyFunc
}

func (r classifiedRows) Scan(dest ...any) error { return r.classifyNil(r.Rows.Scan(dest...)) }
func (r classifiedRows) Err() error             { return r.classifyNil(r.Rows.Err()) }
func (r classifiedRows) Close() error           { return r.classifyNil(r.Rows.Close()) }

func (r classifiedRows) classifyNil(err error) error {
	if err == nil {
		return nil
	}
	return r.classify(err)
}

type stmt struct {
	*sql.Stmt
	db *Pool
}

func (s stmt) QueryContext(ctx context.Context, args ...any) (store.Rows, error) {
	return s.db.query(s.Stmt.QueryContext(ctx, args...))
}

func (s stmt) ExecContext(ctx context.Context, args ...any) (store.Result, error) {
	return s.db.result(s.Stmt.ExecContext(ctx, args...))
}

type tx struct {
	*sql.Tx
	db *Pool
}

func (t tx) QueryContext(ctx context.Context, query string, args ...any) (store.Rows, error) {
	return t.db.query(t.Tx.QueryContext(ctx, query, args...))
}

func (t tx) ExecContext(ctx context.Context, query string, args ...any) (store.Result, error) {
	return t.db.result(t.Tx.ExecContext(ctx, query, args...))
}

func isolation(l store.IsolationLevel) sql.IsolationLevel {
	switch l {
	case store.LevelReadUncommitted:
		return sql.LevelReadUncommitted
	case store.LevelReadCommitted:
		return sql.LevelReadCommitted
	case store.LevelRepeatableRead:
		return sql.LevelRepeatableRead
	case store.LevelSerializable:
		return sql.LevelSerializable
	}
	return sql.LevelDefault
}

// Each runs query on db and calls scan for every row.
func Each(ctx context.Context, db *sql.DB, query string, args []any, scan func(*sql.Rows) error) error {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}
