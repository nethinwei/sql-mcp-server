package mysql

import (
	"database/sql"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/core/provider"
	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
	"github.com/nethinwei/sql-mcp-server/x/providers/sqladapter"
)

func init() {
	providerregistry.Register("mysql", func(dsn string, opts providerregistry.Options) (provider.Provider, error) {
		return Open(dsn, opts)
	})
}

// Provider adapts a MySQL database to the core interfaces.
type Provider struct {
	*sqladapter.Pool
	dialect      dialect.Dialect
	explainer    cost.Explainer
	introspector introspect.Introspector
}

// NewWithTimeout opens MySQL with a DB-native SELECT timeout.
func NewWithTimeout(dsn string, timeout time.Duration) (*Provider, error) {
	return Open(dsn, providerregistry.Options{Timeout: timeout})
}

// Open opens MySQL with opts (see NewAdapter).
func Open(dsn string, opts providerregistry.Options) (*Provider, error) {
	ad, err := NewAdapter(dsn, opts, "")
	if err != nil {
		return nil, err
	}
	return &Provider{
		Pool:         ad,
		dialect:      Dialect{},
		explainer:    mysqlExplainer{db: ad.DB()},
		introspector: NewIntrospector(ad.DB()),
	}, nil
}

// bootstrap sizes and pings the pool through DB(); keep it reachable (an
// embedded field named DB would shadow it).
var _ interface{ DB() *sql.DB } = (*Provider)(nil)

// Dialect returns the MySQL dialect.
func (p *Provider) Dialect() dialect.Dialect { return p.dialect }

// Explainer returns the EXPLAIN-based plan estimator.
func (p *Provider) Explainer() cost.Explainer { return p.explainer }

// Introspector returns the schema introspector.
func (p *Provider) Introspector() introspect.Introspector { return p.introspector }
