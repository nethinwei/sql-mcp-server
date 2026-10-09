package mysql

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	mysqldriver "github.com/go-sql-driver/mysql"

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
func NewAdapter(dsn string, opts providerregistry.Options, oceanBaseVariable string) (*sqladapter.Pool, error) {
	timeout := opts.Timeout
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
	if timeout <= 0 {
		return nil, errors.New("mysql: statement timeout must be positive")
	}
	if opts.Pooler == providerregistry.PoolerTransaction {
		cfg.InterpolateParams = true
	}
	if oceanBaseVariable == "" {
		if _, ok := cfg.Params["max_execution_time"]; !ok {
			cfg.Params["max_execution_time"] = strconv.FormatInt(timeout.Milliseconds(), 10)
		}
	} else if _, ok := cfg.Params[oceanBaseVariable]; !ok {
		cfg.Params[oceanBaseVariable] = strconv.FormatInt(timeout.Microseconds(), 10)
	}
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(opts.OpenContext()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("mysql: ping failed: %w", err)
	}
	return sqladapter.New(db, newTextRows, classify), nil
}

// binaryTypes are the column types whose values are bytes, not text.
var binaryTypes = map[string]bool{
	"BINARY": true, "VARBINARY": true, "BIT": true, "GEOMETRY": true,
	"BLOB": true, "TINYBLOB": true, "MEDIUMBLOB": true, "LONGBLOB": true,
}

// textRows returns text columns as strings. The driver scans them (and
// DECIMAL) into []byte, which would otherwise serialize to clients as base64.
type textRows struct {
	*sql.Rows
	binary []bool
}

func newTextRows(rows *sql.Rows) store.Rows { return &textRows{Rows: rows} }

func (r *textRows) Scan(dest ...any) error {
	if err := r.Rows.Scan(dest...); err != nil {
		return err
	}
	if r.binary == nil {
		types, err := r.ColumnTypes()
		if err != nil {
			return err
		}
		r.binary = make([]bool, len(types))
		for i, t := range types {
			r.binary[i] = binaryTypes[t.DatabaseTypeName()]
		}
	}
	for i, d := range dest {
		if p, ok := d.(*any); ok && i < len(r.binary) && !r.binary[i] {
			if b, ok := (*p).([]byte); ok {
				*p = string(b)
			}
		}
	}
	return nil
}
