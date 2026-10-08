package oceanbase

import (
	"database/sql"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/core/provider"
	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
	"github.com/nethinwei/sql-mcp-server/x/providers/mysql"
	"github.com/nethinwei/sql-mcp-server/x/providers/sqladapter"
)

func init() {
	providerregistry.Register("oceanbase", func(dsn string, timeout time.Duration) (provider.Provider, error) {
		return NewWithTimeout(dsn, timeout)
	})
}

// Provider adapts an OceanBase database to the core interfaces.
type Provider struct {
	*sqladapter.Pool
	dialect      dialect.Dialect
	explainer    cost.Explainer
	introspector introspect.Introspector
}

// NewWithTimeout opens an OceanBase database (MySQL-protocol DSN) with a
// DB-native query timeout and assembles the core adapters.
func NewWithTimeout(dsn string, timeout time.Duration) (*Provider, error) {
	ad, err := mysql.NewAdapterWithTimeout(dsn, timeout, "ob_query_timeout")
	if err != nil {
		return nil, err
	}
	return &Provider{
		Pool:         ad,
		dialect:      Dialect{},
		explainer:    obExplainer{db: ad.DB()},
		introspector: mysql.NewIntrospector(ad.DB()),
	}, nil
}

// bootstrap sizes and pings the pool through DB(); keep it reachable (an
// embedded field named DB would shadow it).
var _ interface{ DB() *sql.DB } = (*Provider)(nil)

// Dialect returns the OceanBase dialect.
func (p *Provider) Dialect() dialect.Dialect { return p.dialect }

// Explainer returns the EXPLAIN-based plan estimator.
func (p *Provider) Explainer() cost.Explainer { return p.explainer }

// Introspector returns the schema introspector (reuses mysql's).
func (p *Provider) Introspector() introspect.Introspector { return p.introspector }
