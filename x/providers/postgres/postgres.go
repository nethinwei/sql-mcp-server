package postgres

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/core/provider"
	"github.com/nethinwei/sql-mcp-server/core/reason"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
	"github.com/nethinwei/sql-mcp-server/x/providers/sqladapter"
)

// ErrPing is returned when the database is unreachable at open time.
var ErrPing = errors.New("postgres: ping failed")

// connectTimeout bounds a connection attempt whose DSN sets no
// connect_timeout: a server that never answers fails the open rather than
// holding startup.
var connectTimeout = 10 * time.Second

// connectTimeoutKeyword finds connect_timeout in a keyword/value DSN.
var connectTimeoutKeyword = regexp.MustCompile(`(^|\s)connect_timeout\s*=`)

// connectTimeoutSet tells whether the DSN, or PGCONNECT_TIMEOUT, sets
// connect_timeout: 0 there waits without limit, as in libpq, while pgx
// parses an unset one as 0 too.
func connectTimeoutSet(dsn string) bool {
	if os.Getenv("PGCONNECT_TIMEOUT") != "" {
		return true
	}
	if u, err := url.Parse(dsn); err == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql") {
		return u.Query().Has("connect_timeout")
	}
	return connectTimeoutKeyword.MatchString(dsn)
}

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
	if opts.Timeout < 0 {
		return nil, errors.New("postgres: statement timeout must not be negative")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse dsn: %w", err)
	}
	if cfg.RuntimeParams, err = sqladapter.SessionParams(cfg.RuntimeParams); err != nil {
		return nil, fmt.Errorf("postgres: parse dsn: %w", err)
	}
	if !connectTimeoutSet(dsn) {
		cfg.ConnectTimeout = connectTimeout
	}
	caps := limits(cfg.RuntimeParams, opts)
	if opts.Pooler == providerregistry.PoolerTransaction {
		cfg.DefaultQueryExecMode = pgx.QueryExecModeExec
	} else if _, configured := cfg.RuntimeParams["statement_timeout"]; !configured && opts.Timeout > 0 {
		cfg.RuntimeParams["statement_timeout"] = strconv.FormatInt(opts.NativeTimeout().Milliseconds(), 10)
	}
	db := stdlib.OpenDB(*cfg, stdlib.OptionAfterConnect(checkConn))
	if !opts.Lazy {
		if err := db.PingContext(opts.OpenContext()); err != nil {
			_ = db.Close()
			if errors.Is(err, ErrUnsupportedServer) {
				return nil, err
			}
			return nil, fmt.Errorf("%w: %v", ErrPing, err)
		}
	}

	pool := sqladapter.New(db, sqladapter.TextRows(binaryTypes), classify)
	p := &Provider{Base: sqladapter.NewBase(pool, Dialect{}, pgExplainer{pool: pool},
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
	params, err := sqladapter.SessionParams(cfg.RuntimeParams)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse dsn: %w", err)
	}
	return limits(params, opts), nil
}

// limits judges the timeouts against the DSN's own startup parameters. When
// a request's deadline passes, pgx drops the connection and sends the server
// a cancel request first, which stops the statement at cost.queryTimeout
// whatever statement_timeout says. Behind a transaction pooler the injected
// statement_timeout is not kept and the cancel request reaches the database
// only if the pooler forwards it (pgbouncer does; this is not tested), so
// the timeouts are best effort there. PostgreSQL refuses no unkeyed write by
// itself. Without a timeout (opts.Timeout 0) none is set (see
// providerregistry.Options.WithoutTimeout).
func limits(params map[string]string, opts providerregistry.Options) dialect.Profile {
	pooler := opts.Pooler
	timeout := dialect.Claim{Strength: dialect.Enforced, Evidence: dialect.DBVerified,
		Reason: reason.New(reason.PostgresTimeout)}
	if pooler == providerregistry.PoolerTransaction {
		timeout = dialect.Claim{Strength: dialect.BestEffort, Evidence: dialect.Declared,
			Reason: reason.New(reason.PostgresTimeoutPooled)}
	} else if _, set := params["statement_timeout"]; set {
		timeout = dialect.Claim{Strength: dialect.Enforced, Evidence: dialect.DBVerified,
			Reason: reason.New(reason.PostgresCancelOnly)}
	}
	return opts.WithoutTimeout(dialect.Profile{
		dialect.TimeoutRead:  timeout,
		dialect.TimeoutWrite: timeout,
		dialect.UnkeyedWrite: {Strength: dialect.Unsupported, Evidence: dialect.Declared,
			Reason: reason.New(reason.UnkeyedGateOnly)},
	})
}

// binaryTypes are the column types whose values are bytes; pgx scans json,
// jsonb and xml into []byte too, which are text.
var binaryTypes = map[string]bool{"BYTEA": true}

// bootstrap sizes and pings the pool through DB(); keep it reachable (an
// embedded field named DB would shadow it).
var _ interface{ DB() *sql.DB } = (*Provider)(nil)
