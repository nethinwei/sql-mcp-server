package oceanbase

import (
	"database/sql"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/provider"
	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
	"github.com/nethinwei/sql-mcp-server/x/providers/mysql"
	"github.com/nethinwei/sql-mcp-server/x/providers/sqladapter"
)

func init() {
	providerregistry.Register("oceanbase", func(dsn string, opts providerregistry.Options) (provider.Provider, error) {
		return Open(dsn, opts)
	})
}

// Provider adapts an OceanBase database to the core interfaces.
type Provider struct{ sqladapter.Base }

// NewWithTimeout opens an OceanBase database (MySQL-protocol DSN) with a
// DB-native query timeout and assembles the core adapters.
func NewWithTimeout(dsn string, timeout time.Duration) (*Provider, error) {
	return Open(dsn, providerregistry.Options{Timeout: timeout})
}

// Open opens OceanBase with opts (see mysql.NewAdapter).
func Open(dsn string, opts providerregistry.Options) (*Provider, error) {
	ad, err := mysql.NewAdapter(dsn, opts, "ob_query_timeout")
	if err != nil {
		return nil, err
	}
	return &Provider{sqladapter.NewBase(ad, Dialect{}, obExplainer{db: ad.DB()}, mysql.NewIntrospector(ad.DB()))}, nil
}

// bootstrap sizes and pings the pool through DB(); keep it reachable (an
// embedded field named DB would shadow it).
var _ interface{ DB() *sql.DB } = (*Provider)(nil)
