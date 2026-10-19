package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"sync/atomic"
	"time"
)

// A cancelled request only closes the client connection: the server runs
// the statement on, and an UPDATE or DELETE keeps its row locks and commits.
// max_execution_time stops SELECT only. So, as PostgreSQL sends a cancel
// request at the request deadline, each connection learns its thread id
// when it opens, and a statement whose context ends before it does is
// stopped with KILL QUERY <id> over a separate connection.
//
// The id is the server's own thread: ids are not reused while the server
// runs (they count up), so a late KILL QUERY reaches the same thread or none.
// A connection that was sent one is never used again: the driver discards
// it when the cancel interrupted its statement, and IsValid reports it bad
// otherwise (a KILL QUERY racing the statement's end).

// killTimeout bounds a KILL QUERY; the statement's own deadline has passed.
const killTimeout = 2 * time.Second

// killer opens connections that stop statements: it holds the pool those
// KILL QUERY statements run on, opened on first use.
type killer struct {
	base driver.Connector
	once sync.Once
	db   *sql.DB
}

func (k *killer) pool() *sql.DB {
	k.once.Do(func() {
		k.db = sql.OpenDB(k.base)
		k.db.SetMaxOpenConns(2)
		k.db.SetMaxIdleConns(1)
		k.db.SetConnMaxIdleTime(time.Minute)
	})
	return k.db
}

// kill stops the statement thread id runs; failing, it only logs.
func (k *killer) kill(id uint64) {
	ctx, cancel := context.WithTimeout(context.Background(), killTimeout)
	defer cancel()
	db := k.pool()
	if db == nil { // closed
		return
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("KILL QUERY %d", id)); err != nil {
		slog.Warn("mysql: stopping a statement past its deadline failed", "thread", id, "error", err.Error())
	}
}

// Close closes the KILL QUERY pool, if it was opened.
func (k *killer) Close() error {
	k.once.Do(func() {}) // a pool opened later would leak
	if k.db != nil {
		return k.db.Close()
	}
	return nil
}

// killConnector opens connections whose statements stop at their context.
type killConnector struct {
	base driver.Connector
	k    *killer
}

func newKillConnector(base driver.Connector) *killConnector {
	return &killConnector{base: base, k: &killer{base: base}}
}

func (c *killConnector) Driver() driver.Driver { return c.base.Driver() }

// Close implements io.Closer: sql.DB closes its connector with itself.
func (c *killConnector) Close() error { return c.k.Close() }

func (c *killConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	id, err := threadID(ctx, conn)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("mysql: read the connection's thread id: %w", err)
	}
	return &killConn{conn: conn, id: id, k: c.k}, nil
}

// threadID is CONNECTION_ID() of conn.
func threadID(ctx context.Context, conn driver.Conn) (uint64, error) {
	q, ok := conn.(driver.QueryerContext)
	if !ok {
		return 0, fmt.Errorf("%T cannot query", conn)
	}
	rows, err := q.QueryContext(ctx, "SELECT CONNECTION_ID()", nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	dest := make([]driver.Value, 1)
	if err := rows.Next(dest); err != nil {
		return 0, err
	}
	switch v := dest[0].(type) {
	case int64:
		return uint64(v), nil
	case uint64:
		return v, nil
	case []byte:
		var id uint64
		_, err := fmt.Sscan(string(v), &id)
		return id, err
	}
	return 0, fmt.Errorf("CONNECTION_ID() returned %T", dest[0])
}

// killConn is a driver connection that stops its statement, by thread id,
// when the statement's context ends first.
type killConn struct {
	conn   driver.Conn
	id     uint64
	k      *killer
	killed atomic.Bool
}

// watch starts watching ctx for the statement about to run; stop ends it as
// the statement (or its rows) is done.
func (c *killConn) watch(ctx context.Context) (stop func()) {
	if ctx.Done() == nil {
		return func() {}
	}
	// context.AfterFunc, not the execution bus: a provider has none, and the
	// function is bounded by killTimeout. Stopped before it starts, it never
	// runs; started, it runs to its end.
	stopKill := context.AfterFunc(ctx, func() {
		c.killed.Store(true)
		c.k.kill(c.id)
	})
	return func() { stopKill() }
}

func (c *killConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	stop := c.watch(ctx)
	defer stop()
	return c.conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c *killConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	stop := c.watch(ctx)
	rows, err := c.conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err != nil {
		stop()
		return nil, err
	}
	return &killRows{Rows: rows, stop: stop}, nil
}

func (c *killConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	s, err := c.conn.(driver.ConnPrepareContext).PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return &killStmt{stmt: s, c: c}, nil
}

func (c *killConn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

func (c *killConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}

//nolint:staticcheck // driver.Conn requires Begin; BeginTx is what database/sql calls.
func (c *killConn) Begin() (driver.Tx, error) { return c.conn.Begin() }

func (c *killConn) Close() error { return c.conn.Close() }

func (c *killConn) Ping(ctx context.Context) error { return c.conn.(driver.Pinger).Ping(ctx) }

func (c *killConn) CheckNamedValue(nv *driver.NamedValue) error {
	return c.conn.(driver.NamedValueChecker).CheckNamedValue(nv)
}

// ResetSession and IsValid keep a connection sent a KILL QUERY out of the
// pool.
func (c *killConn) ResetSession(ctx context.Context) error {
	if c.killed.Load() {
		return driver.ErrBadConn
	}
	return c.conn.(driver.SessionResetter).ResetSession(ctx)
}

func (c *killConn) IsValid() bool {
	return !c.killed.Load() && c.conn.(driver.Validator).IsValid()
}

// killStmt is a prepared statement of a killConn.
type killStmt struct {
	stmt driver.Stmt
	c    *killConn
}

func (s *killStmt) Close() error  { return s.stmt.Close() }
func (s *killStmt) NumInput() int { return s.stmt.NumInput() }

//nolint:staticcheck // driver.Stmt requires Exec; ExecContext is what database/sql calls.
func (s *killStmt) Exec(args []driver.Value) (driver.Result, error) { return s.stmt.Exec(args) }

//nolint:staticcheck // driver.Stmt requires Query; QueryContext is what database/sql calls.
func (s *killStmt) Query(args []driver.Value) (driver.Rows, error) { return s.stmt.Query(args) }

func (s *killStmt) CheckNamedValue(nv *driver.NamedValue) error {
	return s.stmt.(driver.NamedValueChecker).CheckNamedValue(nv)
}

func (s *killStmt) ColumnConverter(idx int) driver.ValueConverter {
	return s.stmt.(driver.ColumnConverter).ColumnConverter(idx) //nolint:staticcheck // the driver's own
}

func (s *killStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	stop := s.c.watch(ctx)
	defer stop()
	return s.stmt.(driver.StmtExecContext).ExecContext(ctx, args)
}

func (s *killStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	stop := s.c.watch(ctx)
	rows, err := s.stmt.(driver.StmtQueryContext).QueryContext(ctx, args)
	if err != nil {
		stop()
		return nil, err
	}
	return &killRows{Rows: rows, stop: stop}, nil
}

// killRows keeps watching the statement while its rows are read (a CALL
// runs on as its results stream), forwarding what the driver's rows report.
type killRows struct {
	driver.Rows
	stop func()
}

func (r *killRows) Close() error {
	defer r.stop()
	return r.Rows.Close()
}

func (r *killRows) HasNextResultSet() bool {
	n, ok := r.Rows.(driver.RowsNextResultSet)
	return ok && n.HasNextResultSet()
}

func (r *killRows) NextResultSet() error {
	if n, ok := r.Rows.(driver.RowsNextResultSet); ok {
		return n.NextResultSet()
	}
	return io.EOF
}

func (r *killRows) ColumnTypeDatabaseTypeName(i int) string {
	if t, ok := r.Rows.(driver.RowsColumnTypeDatabaseTypeName); ok {
		return t.ColumnTypeDatabaseTypeName(i)
	}
	return ""
}

func (r *killRows) ColumnTypeNullable(i int) (nullable, ok bool) {
	if t, has := r.Rows.(driver.RowsColumnTypeNullable); has {
		return t.ColumnTypeNullable(i)
	}
	return false, false
}

func (r *killRows) ColumnTypePrecisionScale(i int) (precision, scale int64, ok bool) {
	if t, has := r.Rows.(driver.RowsColumnTypePrecisionScale); has {
		return t.ColumnTypePrecisionScale(i)
	}
	return 0, 0, false
}

func (r *killRows) ColumnTypeScanType(i int) reflect.Type {
	if t, ok := r.Rows.(driver.RowsColumnTypeScanType); ok {
		return t.ColumnTypeScanType(i)
	}
	return reflect.TypeFor[any]()
}
