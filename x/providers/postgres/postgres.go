package postgres

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/nethinwei/sql-mcp-server/core/dialect"
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
	providerregistry.RegisterLimits("postgres", Limits)
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
	caps := limits(cfg.RuntimeParams, opts.Pooler)
	if opts.Pooler == providerregistry.PoolerTransaction {
		cfg.DefaultQueryExecMode = pgx.QueryExecModeExec
	} else if _, configured := cfg.RuntimeParams["statement_timeout"]; !configured {
		cfg.RuntimeParams["statement_timeout"] = strconv.FormatInt(opts.NativeTimeout().Milliseconds(), 10)
	}
	db := stdlib.OpenDB(*cfg)
	if err := db.PingContext(opts.OpenContext()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("%w: %v", ErrPing, err)
	}
	p := &Provider{Base: sqladapter.NewBase(sqladapter.New(db, nil, classify), Dialect{}, pgExplainer{db: db},
		pgIntrospector{db: db}).WithLimits(caps)}
	p.analyzeTx = p
	return p, nil
}

// Limits is what a PostgreSQL connection to dsn opened with opts guarantees
// on the server, without connecting.
func Limits(dsn string, opts providerregistry.Options) (dialect.Profile, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse dsn: %w", err)
	}
	return limits(cfg.RuntimeParams, opts.Pooler), nil
}

// limits judges the timeouts against the DSN's own startup parameters. When
// a request's deadline passes, pgx drops the connection and sends the server
// a cancel request first, which stops the statement at cost.queryTimeout
// whatever statement_timeout says. Behind a transaction pooler the injected
// statement_timeout is not kept and the cancel request reaches the database
// only if the pooler forwards it (pgbouncer does; this is not tested), so
// the timeouts are best effort there. PostgreSQL refuses no unkeyed write by
// itself.
func limits(params map[string]string, pooler string) dialect.Profile {
	timeout := dialect.Claim{Strength: dialect.Enforced, Evidence: dialect.DBVerified,
		Reason: "statement_timeout just under cost.queryTimeout, and a cancel request at the request deadline"}
	if pooler == providerregistry.PoolerTransaction {
		timeout = dialect.Claim{Strength: dialect.BestEffort, Evidence: dialect.Declared,
			Reason: "statement_timeout is a session setting a transaction pooler does not keep; " +
				"a cancel request at the request deadline stops the statement only if the pooler forwards it"}
	} else if _, set := params["statement_timeout"]; set {
		timeout = dialect.Claim{Strength: dialect.Enforced, Evidence: dialect.DBVerified,
			Reason: "a cancel request at the request deadline (the DSN sets its own statement_timeout)"}
	}
	return dialect.Profile{
		dialect.TimeoutRead:  timeout,
		dialect.TimeoutWrite: timeout,
		dialect.UnkeyedWrite: {Strength: dialect.Unsupported, Evidence: dialect.Declared,
			Reason: "no database-side refusal; the gate's WriteGuard refuses unkeyed writes"},
	}
}

// bootstrap sizes and pings the pool through DB(); keep it reachable (an
// embedded field named DB would shadow it).
var _ interface{ DB() *sql.DB } = (*Provider)(nil)
