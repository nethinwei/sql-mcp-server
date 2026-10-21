package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"

	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
	"github.com/nethinwei/sql-mcp-server/x/providers/sqladapter"
)

// NewAdapter opens a MySQL-compatible database and pings it (fail-fast). It
// injects sql_safe_updates=1 as a DB-native backstop against full-table
// UPDATE/DELETE (defense in depth alongside the cost gate's WriteGuard) and a
// DB-native statement timeout: max_execution_time, or the named OceanBase
// variable when oceanBaseVariable is set. DSN values set explicitly are
// respected. Behind a transaction-mode pooler arguments are interpolated by
// the driver instead of server-side prepared statements, which a pooler may
// not keep. The adapter is shared with the oceanbase provider.
//
// On MySQL a statement still running at its context's end is stopped with
// KILL QUERY (see killConn): max_execution_time stops SELECT only.
//
// It returns the connection's limits as well: what those settings guarantee
// given what the DSN already set (see Limits).
func NewAdapter(
	dsn string, opts providerregistry.Options, oceanBaseVariable string,
) (*sqladapter.Pool, dialect.Profile, error) {
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("mysql: parse dsn: %w", err)
	}
	if cfg.Params, err = sqladapter.SessionParams(cfg.Params); err != nil {
		return nil, nil, fmt.Errorf("mysql: parse dsn: %w", err)
	}
	caps := limits(cfg.Params, opts, oceanBaseVariable)
	sessionDefaults(cfg, dsn)
	ioTimeouts(cfg, dsn, opts)
	if opts.Timeout < 0 {
		return nil, nil, errors.New("mysql: statement timeout must not be negative")
	}
	if opts.Pooler == providerregistry.PoolerTransaction {
		cfg.InterpolateParams = true
	}
	// Without a timeout none is set (see providerregistry.Options.WithoutTimeout).
	if _, ok := cfg.Params["max_execution_time"]; !ok && opts.Timeout > 0 && oceanBaseVariable == "" {
		cfg.Params["max_execution_time"] = strconv.FormatInt(opts.NativeTimeout().Milliseconds(), 10)
	}
	if _, ok := cfg.Params[oceanBaseVariable]; !ok && opts.Timeout > 0 && oceanBaseVariable != "" {
		cfg.Params[oceanBaseVariable] = strconv.FormatInt(opts.NativeTimeout().Microseconds(), 10)
	}
	base, err := mysqldriver.NewConnector(cfg)
	if err != nil {
		return nil, nil, err
	}
	var connector driver.Connector = dialConnector{base}
	if killsStatements(opts.Pooler, oceanBaseVariable) {
		connector = newKillConnector(connector)
	}
	db := sql.OpenDB(connector)
	if !opts.Lazy {
		if err := db.PingContext(opts.OpenContext()); err != nil {
			_ = db.Close()
			return nil, nil, fmt.Errorf("mysql: ping failed: %w", err)
		}
	}
	classifier := classify
	if oceanBaseVariable != "" {
		classifier = classifyOceanBase
	}
	return sqladapter.New(db, sqladapter.TypedTextRows(binaryTypes, unsignedTypes), classifier), caps, nil
}

// sessionDefaults sets what a DSN leaves unset: rowsAffected counts the rows
// a statement matched, as on PostgreSQL, not those it changed; and updates
// and deletes without a key or a limit are refused (sql_safe_updates).
func sessionDefaults(cfg *mysqldriver.Config, dsn string) {
	if !strings.Contains(dsn, "clientFoundRows=") {
		cfg.ClientFoundRows = true
	}
	if _, ok := cfg.Params["sql_safe_updates"]; !ok {
		cfg.Params["sql_safe_updates"] = "1"
	}
}

// dialTimeout bounds a connection attempt whose DSN sets no timeout: a
// server that never answers its handshake fails the open rather than
// holding startup or a reload (as PostgreSQL's connect_timeout default).
const dialTimeout = 10 * time.Second

// ioTimeouts sets what a DSN leaves unset of the driver's I/O timeouts: the
// dial's (dialTimeout), and with a statement timeout a read timeout past it
// by as much, so that a server that stops answering mid-statement breaks the
// connection instead of holding it, while one stopping the statement (its
// timeout, KILL QUERY) answers first.
func ioTimeouts(cfg *mysqldriver.Config, dsn string, opts providerregistry.Options) {
	if !dsnSets(dsn, "timeout") {
		cfg.Timeout = dialTimeout
	}
	if !dsnSets(dsn, "readTimeout") && opts.Timeout > 0 {
		cfg.ReadTimeout = opts.Timeout + dialTimeout
	}
}

// dsnSets tells whether dsn sets the parameter name (exactly: timeout is not
// readTimeout). The parameters follow the first ? after the DSN's last /, as
// the driver reads them.
func dsnSets(dsn, name string) bool {
	_, params, _ := strings.Cut(dsn[strings.LastIndexByte(dsn, '/')+1:], "?")
	for param := range strings.SplitSeq(params, "&") {
		if key, _, _ := strings.Cut(param, "="); key == name {
			return true
		}
	}
	return false
}

// binaryTypes are the column types whose values are bytes, not text.
var binaryTypes = map[string]bool{
	"BINARY": true, "VARBINARY": true, "BIT": true, "GEOMETRY": true,
	"BLOB": true, "TINYBLOB": true, "MEDIUMBLOB": true, "LONGBLOB": true,
}

// unsignedTypes are the column types the driver types by protocol (see
// sqladapter.TypedTextRows); its other integers are int64 either way.
var unsignedTypes = map[string]bool{"UNSIGNED BIGINT": true}

// dialConnector marks a connection that could not be made as such
// (store.ErrNotConnected): the driver returns the network error of a dial,
// a TLS handshake or a refused login bare, which would otherwise read as a
// statement whose connection broke while it ran, its outcome unknown. A
// driver.ErrBadConn is left as it is: database/sql tries another
// connection for it.
type dialConnector struct{ driver.Connector }

func (c dialConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil && !errors.Is(err, driver.ErrBadConn) {
		return nil, fmt.Errorf("%w: %w", store.ErrNotConnected, err)
	}
	return conn, err
}

// Close closes the driver's connector, when it has something to close.
func (c dialConnector) Close() error {
	if closer, ok := c.Connector.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}
