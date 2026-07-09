package oceanbase

import (
	"github.com/nethinwei/sql-mcp-server/cost"
	"github.com/nethinwei/sql-mcp-server/dialect"
	"github.com/nethinwei/sql-mcp-server/introspect"
	"github.com/nethinwei/sql-mcp-server/x/providers/mysql"
)

// Provider adapts an OceanBase database to the core interfaces.
type Provider struct {
	*mysql.Adapter
	dialect      dialect.Dialect
	explainer    cost.Explainer
	introspector introspect.Introspector
}

// New opens an OceanBase database (MySQL-protocol DSN) and assembles adapters.
func New(dsn string) (*Provider, error) {
	ad, err := mysql.NewAdapter(dsn)
	if err != nil {
		return nil, err
	}
	return &Provider{
		Adapter:      ad,
		dialect:      dialect.OceanBase{},
		explainer:    obExplainer{db: ad.DB()},
		introspector: mysql.NewIntrospector(ad.DB()),
	}, nil
}

// Dialect returns the OceanBase dialect.
func (p *Provider) Dialect() dialect.Dialect { return p.dialect }

// Explainer returns the EXPLAIN-based plan estimator.
func (p *Provider) Explainer() cost.Explainer { return p.explainer }

// Introspector returns the schema introspector (reuses mysql's).
func (p *Provider) Introspector() introspect.Introspector { return p.introspector }
