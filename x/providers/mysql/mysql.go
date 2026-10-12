package mysql

import (
	"database/sql"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/provider"
	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
	"github.com/nethinwei/sql-mcp-server/x/providers/sqladapter"
)

func init() {
	providerregistry.Register("mysql", func(dsn string, opts providerregistry.Options) (provider.Provider, error) {
		return Open(dsn, opts)
	})
	providerregistry.RegisterLimits("mysql", Limits)
}

// Provider adapts a MySQL database to the core interfaces.
type Provider struct{ sqladapter.Base }

// NewWithTimeout opens MySQL with a DB-native SELECT timeout.
func NewWithTimeout(dsn string, timeout time.Duration) (*Provider, error) {
	return Open(dsn, providerregistry.Options{Timeout: timeout})
}

// Open opens MySQL with opts (see NewAdapter).
func Open(dsn string, opts providerregistry.Options) (*Provider, error) {
	ad, limits, err := NewAdapter(dsn, opts, "")
	if err != nil {
		return nil, err
	}
	base := sqladapter.NewBase(ad, Dialect{}, mysqlExplainer{db: ad.DB()}, NewIntrospector(ad.DB()))
	return &Provider{base.WithLimits(limits)}, nil
}

// bootstrap sizes and pings the pool through DB(); keep it reachable (an
// embedded field named DB would shadow it).
var _ interface{ DB() *sql.DB } = (*Provider)(nil)
