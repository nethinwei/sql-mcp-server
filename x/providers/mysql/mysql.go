package mysql

import (
	"database/sql"
	"time"

	"github.com/nethinwei/sql-mcp-server/cost"
	"github.com/nethinwei/sql-mcp-server/dialect"
	"github.com/nethinwei/sql-mcp-server/introspect"
)

// Provider adapts a MySQL database to the core interfaces.
type Provider struct {
	*Adapter
	dialect      dialect.Dialect
	explainer    cost.Explainer
	introspector introspect.Introspector
}

// New opens a MySQL database and assembles the core adapters.
func New(dsn string) (*Provider, error) {
	return NewWithTimeout(dsn, 30*time.Second)
}

// NewWithTimeout opens MySQL with a DB-native SELECT timeout.
func NewWithTimeout(dsn string, timeout time.Duration) (*Provider, error) {
	ad, err := NewAdapterWithTimeout(dsn, timeout, "")
	if err != nil {
		return nil, err
	}
	return &Provider{
		Adapter:      ad,
		dialect:      Dialect{},
		explainer:    mysqlExplainer{db: ad.db},
		introspector: NewIntrospector(ad.db),
	}, nil
}

// Dialect returns the MySQL dialect.
func (p *Provider) Dialect() dialect.Dialect { return p.dialect }

// Explainer returns the EXPLAIN-based plan estimator.
func (p *Provider) Explainer() cost.Explainer { return p.explainer }

// Introspector returns the schema introspector.
func (p *Provider) Introspector() introspect.Introspector { return p.introspector }

// compile-time assertion that *sql.DB is available to the explainer/introspector.
var _ = (*sql.DB)(nil)
