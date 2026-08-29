// Package hologres adapts Alibaba Cloud Hologres, a PostgreSQL-wire-compatible
// real-time warehouse (server lineage 11), to the core interfaces.
//
// Compatibility is protocol-level, not semantic: Hologres transactions cover
// DDL only, so data statements cannot be atomic and begin_transaction fails
// closed (Capability Transaction=false). Its EXPLAIN output is text and its
// estimates are unverified here, so the cost gate is assembled without an
// Estimate layer and leans on EnforceCap + statement_timeout. Only the simple
// query protocol is in Hologres' verified compatibility envelope, so the pool
// defaults to it unless the DSN pins default_query_exec_mode explicitly.
package hologres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/core/provider"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
	"github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

func init() {
	providerregistry.Register("hologres", func(dsn string, timeout time.Duration) (provider.Provider, error) {
		return NewWithTimeout(dsn, timeout)
	})
}

// Provider adapts a Hologres database to the core interfaces. It delegates the
// wire-level store adapters to the postgres provider (same driver, same
// protocol) but holds it as a named field: embedding would also inherit the
// postgres AnalyzeSampler (EXPLAIN (ANALYZE, FORMAT JSON) in a read-only
// transaction), which Hologres neither supports nor verifies.
type Provider struct {
	pg           *postgres.Provider
	dialect      dialect.Dialect
	explainer    cost.Explainer
	introspector introspect.Introspector
}

// New opens a Hologres database and pings it. The DSN uses the pgx format,
// e.g. postgres://$AK_ID:$AK_SECRET@$ENDPOINT:80/$DB.
func New(dsn string) (*Provider, error) {
	return NewWithTimeout(dsn, 30*time.Second)
}

// NewWithTimeout opens Hologres with a DB-native statement timeout.
func NewWithTimeout(dsn string, timeout time.Duration) (*Provider, error) {
	if timeout <= 0 {
		return nil, errors.New("hologres: statement timeout must be positive")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("hologres: parse dsn: %w", err)
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	if _, configured := cfg.RuntimeParams["statement_timeout"]; !configured {
		cfg.RuntimeParams["statement_timeout"] = strconv.FormatInt(timeout.Milliseconds(), 10)
	}
	if _, configured := cfg.RuntimeParams["application_name"]; !configured {
		// Surfaces in Hologres slow-query diagnostics; without a tag this
		// gateway's traffic is indistinguishable from other clients.
		cfg.RuntimeParams["application_name"] = "sql-mcp-server"
	}
	// Only the simple query protocol is inside Hologres' verified envelope
	// (psql/JDBC-class clients); pgx's extended protocol with named prepared
	// statements is not, so default to simple unless the DSN pinned a mode via
	// default_query_exec_mode (pgx consumes it into DefaultQueryExecMode).
	if !strings.Contains(dsn, "default_query_exec_mode") {
		cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	}
	pg, err := postgres.NewWithConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("hologres: %w", err)
	}
	return &Provider{
		pg:           pg,
		dialect:      Dialect{},
		explainer:    hgExplainer{db: pg.DB()},
		introspector: hgIntrospector{db: pg.DB()},
	}, nil
}

// QueryContext implements store.DB.
func (p *Provider) QueryContext(ctx context.Context, query string, args ...any) (store.Rows, error) {
	return p.pg.QueryContext(ctx, query, args...)
}

// ExecContext implements store.DB.
func (p *Provider) ExecContext(ctx context.Context, query string, args ...any) (store.Result, error) {
	return p.pg.ExecContext(ctx, query, args...)
}

// PrepareContext implements store.Preparer (prepared-statement cache).
func (p *Provider) PrepareContext(ctx context.Context, query string) (store.Prepared, error) {
	return p.pg.PrepareContext(ctx, query)
}

// BeginTx implements store.TxBeginner. It is reachable only when the dialect's
// Transaction capability has been cleared by evidence; core's transaction tool
// fails closed before this for Hologres.
func (p *Provider) BeginTx(ctx context.Context, opts *store.TxOptions) (store.Tx, error) {
	return p.pg.BeginTx(ctx, opts)
}

// DB returns the underlying connection pool, for configuration by the
// assembler (e.g. SetMaxOpenConns to bound DB connections).
func (p *Provider) DB() *sql.DB { return p.pg.DB() }

// Dialect returns the Hologres dialect.
func (p *Provider) Dialect() dialect.Dialect { return p.dialect }

// Explainer returns the conservative text-EXPLAIN parser.
func (p *Provider) Explainer() cost.Explainer { return p.explainer }

// Introspector returns the Hologres schema introspector.
func (p *Provider) Introspector() introspect.Introspector { return p.introspector }

// Close closes the underlying connection pool.
func (p *Provider) Close() error { return p.pg.Close() }
