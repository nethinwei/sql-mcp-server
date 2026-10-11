package postgres

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/nethinwei/sql-mcp-server/core/provider"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
	"github.com/nethinwei/sql-mcp-server/x/providers/sqladapter"
)

// ErrPing is returned when the database is unreachable at open time.
var ErrPing = errors.New("postgres: ping failed")

func init() {
	providerregistry.Register("postgres", func(dsn string, opts providerregistry.Options) (provider.Provider, error) {
		return Open(dsn, opts)
	})
}

// Provider adapts a PostgreSQL database to the core interfaces.
type Provider struct {
	sqladapter.Base
	analyzeTx store.TxBeginner
}

// New opens a PostgreSQL database and pings it. The DSN uses the pgx driver
// format. It returns ErrPing (wrapping the cause) if the database is
// unreachable, failing fast rather than starting up broken.
func New(dsn string) (*Provider, error) {
	return NewWithTimeout(dsn, 30*time.Second)
}

// NewWithTimeout opens PostgreSQL with a DB-native statement timeout.
func NewWithTimeout(dsn string, timeout time.Duration) (*Provider, error) {
	return Open(dsn, providerregistry.Options{Timeout: timeout})
}

// Open opens PostgreSQL with opts. The statement timeout is a startup
// parameter, except behind a transaction-mode pooler, which neither forwards
// it nor keeps named prepared statements: there statements run unprepared and
// time out through request cancellation.
func Open(dsn string, opts providerregistry.Options) (*Provider, error) {
	if opts.Timeout <= 0 {
		return nil, errors.New("postgres: statement timeout must be positive")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse dsn: %w", err)
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	if opts.Pooler == providerregistry.PoolerTransaction {
		cfg.DefaultQueryExecMode = pgx.QueryExecModeExec
	} else if _, configured := cfg.RuntimeParams["statement_timeout"]; !configured {
		cfg.RuntimeParams["statement_timeout"] = strconv.FormatInt(opts.Timeout.Milliseconds(), 10)
	}
	db := stdlib.OpenDB(*cfg)
	if err := db.PingContext(opts.OpenContext()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("%w: %v", ErrPing, err)
	}
	p := &Provider{Base: sqladapter.NewBase(sqladapter.New(db, nil, classify), Dialect{}, pgExplainer{db: db},
		pgIntrospector{db: db})}
	p.analyzeTx = p
	return p, nil
}

// bootstrap sizes and pings the pool through DB(); keep it reachable (an
// embedded field named DB would shadow it).
var _ interface{ DB() *sql.DB } = (*Provider)(nil)
