package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/nethinwei/sql-mcp-server/cost"
	"github.com/nethinwei/sql-mcp-server/dialect"
	"github.com/nethinwei/sql-mcp-server/introspect"
	"github.com/nethinwei/sql-mcp-server/store"
)

// ErrPing is returned when the database is unreachable at open time.
var ErrPing = errors.New("postgres: ping failed")

// savepointName validates a savepoint name to prevent SQL injection in
// SAVEPOINT/ROLLBACK TO statements (names are not parameterized in SQL).
var savepointName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// Provider adapts a PostgreSQL database to the core interfaces.
type Provider struct {
	db           *sql.DB
	dialect      dialect.Dialect
	explainer    cost.Explainer
	introspector introspect.Introspector
	analyzeTx    store.TxBeginner
}

// New opens a PostgreSQL database and pings it. The DSN uses the pgx driver
// format. It returns ErrPing (wrapping the cause) if the database is
// unreachable, failing fast rather than starting up broken.
func New(dsn string) (*Provider, error) {
	return NewWithTimeout(dsn, 30*time.Second)
}

// NewWithTimeout opens PostgreSQL with a DB-native statement timeout.
func NewWithTimeout(dsn string, timeout time.Duration) (*Provider, error) {
	if timeout <= 0 {
		return nil, errors.New("postgres: statement timeout must be positive")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse dsn: %w", err)
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	if _, configured := cfg.RuntimeParams["statement_timeout"]; !configured {
		cfg.RuntimeParams["statement_timeout"] = strconv.FormatInt(timeout.Milliseconds(), 10)
	}
	db := stdlib.OpenDB(*cfg)
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("%w: %v", ErrPing, err)
	}
	p := &Provider{
		db:           db,
		dialect:      Dialect{},
		explainer:    pgExplainer{db: db},
		introspector: pgIntrospector{db: db},
	}
	p.analyzeTx = p
	return p, nil
}

// QueryContext implements store.DB.
func (p *Provider) QueryContext(ctx context.Context, query string, args ...any) (store.Rows, error) {
	rows, err := p.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return rowsAdapter{Rows: rows}, nil
}

// ExecContext implements store.DB.
func (p *Provider) ExecContext(ctx context.Context, query string, args ...any) (store.Result, error) {
	res, err := p.db.ExecContext(ctx, query, args...)
	if err != nil {
		return store.Result{}, err
	}
	li, _ := res.LastInsertId()
	ra, _ := res.RowsAffected()
	return store.Result{LastInsertID: li, RowsAffected: ra}, nil
}

// PrepareContext implements store.Preparer.
func (p *Provider) PrepareContext(ctx context.Context, query string) (store.Prepared, error) {
	stmt, err := p.db.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return stmtAdapter{Stmt: stmt}, nil
}

// BeginTx implements store.TxBeginner.
func (p *Provider) BeginTx(ctx context.Context, opts *store.TxOptions) (store.Tx, error) {
	tx, err := p.db.BeginTx(ctx, toSqlOpts(opts))
	if err != nil {
		return nil, err
	}
	return &txAdapter{tx: tx}, nil
}

// DB returns the underlying connection pool, for configuration by the
// assembler (e.g. SetMaxOpenConns to bound DB connections).
func (p *Provider) DB() *sql.DB { return p.db }

// Dialect returns the PostgreSQL dialect.
func (p *Provider) Dialect() dialect.Dialect { return p.dialect }

// Explainer returns the EXPLAIN-based plan estimator.
func (p *Provider) Explainer() cost.Explainer { return p.explainer }

// Introspector returns the schema introspector.
func (p *Provider) Introspector() introspect.Introspector { return p.introspector }

// Close closes the underlying connection pool.
func (p *Provider) Close() error { return p.db.Close() }

// rowsAdapter wraps *sql.Rows to satisfy store.Rows.
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

// txAdapter wraps *sql.Tx to satisfy store.Tx.
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
		return store.Savepoint{}, fmt.Errorf("postgres: invalid savepoint name %q", name)
	}
	if _, err := t.tx.ExecContext(ctx, "SAVEPOINT "+name); err != nil {
		return store.Savepoint{}, err
	}
	return store.Savepoint{Name: name}, nil
}

func (t *txAdapter) RollbackTo(ctx context.Context, sp store.Savepoint) error {
	if !savepointName.MatchString(sp.Name) {
		return fmt.Errorf("postgres: invalid savepoint name %q", sp.Name)
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
