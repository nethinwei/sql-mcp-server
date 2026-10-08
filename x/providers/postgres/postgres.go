package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/core/provider"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
	"github.com/nethinwei/sql-mcp-server/x/providers/sqladapter"
)

// ErrPing is returned when the database is unreachable at open time.
var ErrPing = errors.New("postgres: ping failed")

func init() {
	providerregistry.Register("postgres", func(dsn string, timeout time.Duration) (provider.Provider, error) {
		return NewWithTimeout(dsn, timeout)
	})
}

// Provider adapts a PostgreSQL database to the core interfaces.
type Provider struct {
	*sqladapter.Pool
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
		Pool:         sqladapter.New(db, nil),
		dialect:      Dialect{},
		explainer:    pgExplainer{db: db},
		introspector: pgIntrospector{db: db},
	}
	p.analyzeTx = p
	return p, nil
}

// bootstrap sizes and pings the pool through DB(); keep it reachable (an
// embedded field named DB would shadow it).
var _ interface{ DB() *sql.DB } = (*Provider)(nil)

// Dialect returns the PostgreSQL dialect.
func (p *Provider) Dialect() dialect.Dialect { return p.dialect }

// Explainer returns the EXPLAIN-based plan estimator.
func (p *Provider) Explainer() cost.Explainer { return p.explainer }

// Introspector returns the schema introspector.
func (p *Provider) Introspector() introspect.Introspector { return p.introspector }
