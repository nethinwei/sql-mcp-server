package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"

	mysqldriver "github.com/go-sql-driver/mysql"

	"github.com/nethinwei/sql-mcp-server/store"
)

// savepointName validates a savepoint name (not parameterized in SQL).
var savepointName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// Adapter wraps *sql.DB for the MySQL protocol, satisfying store.DB and
// store.TxBeginner. Shared with the oceanbase provider.
type Adapter struct {
	db *sql.DB
}

// NewAdapter opens a MySQL-compatible database and pings it (fail-fast). It
// injects sql_safe_updates=1 as a DB-native backstop against full-table
// UPDATE/DELETE (defense in depth alongside the cost gate's WriteGuard); a DSN
// that sets sql_safe_updates explicitly is respected.
func NewAdapter(dsn string) (*Adapter, error) {
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("mysql: parse dsn: %w", err)
	}
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	if _, ok := cfg.Params["sql_safe_updates"]; !ok {
		cfg.Params["sql_safe_updates"] = "1"
	}
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("mysql: ping failed: %w", err)
	}
	return &Adapter{db: db}, nil
}

// DB exposes the underlying pool (for providers that need it).
func (a *Adapter) DB() *sql.DB { return a.db }

// QueryContext implements store.DB.
func (a *Adapter) QueryContext(ctx context.Context, query string, args ...any) (store.Rows, error) {
	rows, err := a.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return rowsAdapter{Rows: rows}, nil
}

// ExecContext implements store.DB.
func (a *Adapter) ExecContext(ctx context.Context, query string, args ...any) (store.Result, error) {
	res, err := a.db.ExecContext(ctx, query, args...)
	if err != nil {
		return store.Result{}, err
	}
	li, _ := res.LastInsertId()
	ra, _ := res.RowsAffected()
	return store.Result{LastInsertID: li, RowsAffected: ra}, nil
}

// PrepareContext implements store.Preparer for MySQL and OceanBase.
func (a *Adapter) PrepareContext(ctx context.Context, query string) (store.Prepared, error) {
	stmt, err := a.db.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return stmtAdapter{Stmt: stmt}, nil
}

// BeginTx implements store.TxBeginner.
func (a *Adapter) BeginTx(ctx context.Context, opts *store.TxOptions) (store.Tx, error) {
	tx, err := a.db.BeginTx(ctx, toSqlOpts(opts))
	if err != nil {
		return nil, err
	}
	return &txAdapter{tx: tx}, nil
}

// Close closes the pool.
func (a *Adapter) Close() error { return a.db.Close() }

type rowsAdapter struct{ *sql.Rows }

func (r rowsAdapter) Next() bool                 { return r.Rows.Next() }
func (r rowsAdapter) Scan(dest ...any) error     { return r.Rows.Scan(dest...) }
func (r rowsAdapter) Columns() ([]string, error) { return r.Rows.Columns() }
func (r rowsAdapter) Close() error               { return r.Rows.Close() }
func (r rowsAdapter) Err() error                 { return r.Rows.Err() }

type stmtAdapter struct{ *sql.Stmt }

func (s stmtAdapter) QueryContext(ctx context.Context, args ...any) (store.Rows, error) {
	rows, err := s.Stmt.QueryContext(ctx, args...)
	if err != nil {
		return nil, err
	}
	return rowsAdapter{Rows: rows}, nil
}

func (s stmtAdapter) ExecContext(ctx context.Context, args ...any) (store.Result, error) {
	result, err := s.Stmt.ExecContext(ctx, args...)
	if err != nil {
		return store.Result{}, err
	}
	li, _ := result.LastInsertId()
	ra, _ := result.RowsAffected()
	return store.Result{LastInsertID: li, RowsAffected: ra}, nil
}

type txAdapter struct{ tx *sql.Tx }

func (t *txAdapter) QueryContext(ctx context.Context, query string, args ...any) (store.Rows, error) {
	rows, err := t.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return rowsAdapter{Rows: rows}, nil
}

func (t *txAdapter) ExecContext(ctx context.Context, query string, args ...any) (store.Result, error) {
	res, err := t.tx.ExecContext(ctx, query, args...)
	if err != nil {
		return store.Result{}, err
	}
	li, _ := res.LastInsertId()
	ra, _ := res.RowsAffected()
	return store.Result{LastInsertID: li, RowsAffected: ra}, nil
}

func (t *txAdapter) Commit() error   { return t.tx.Commit() }
func (t *txAdapter) Rollback() error { return t.tx.Rollback() }

func (t *txAdapter) Savepoint(ctx context.Context, name string) (store.Savepoint, error) {
	if !savepointName.MatchString(name) {
		return store.Savepoint{}, fmt.Errorf("mysql: invalid savepoint name %q", name)
	}
	if _, err := t.tx.ExecContext(ctx, "SAVEPOINT "+name); err != nil {
		return store.Savepoint{}, err
	}
	return store.Savepoint{Name: name}, nil
}

func (t *txAdapter) RollbackTo(ctx context.Context, sp store.Savepoint) error {
	if !savepointName.MatchString(sp.Name) {
		return fmt.Errorf("mysql: invalid savepoint name %q", sp.Name)
	}
	_, err := t.tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+sp.Name)
	return err
}

func toSqlOpts(o *store.TxOptions) *sql.TxOptions {
	if o == nil {
		return nil
	}
	return &sql.TxOptions{Isolation: toSqlIsolation(o.Isolation), ReadOnly: o.ReadOnly}
}

func toSqlIsolation(l store.IsolationLevel) sql.IsolationLevel {
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
