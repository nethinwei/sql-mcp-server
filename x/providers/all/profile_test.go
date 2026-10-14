package all

import (
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
	"github.com/nethinwei/sql-mcp-server/x/providers/mysql"
	"github.com/nethinwei/sql-mcp-server/x/providers/oceanbase"
	"github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

// A connection's profile is its dialect's claims With its limits', which
// replace the dialect's on a shared key: the two key spaces stay apart, the
// dialect claiming no limit.* key and the limits nothing else, so neither
// silently overrides the other.
func TestDialectAndLimitClaimsDoNotOverlap(t *testing.T) {
	t.Parallel()
	dsns := map[string]string{
		"postgres": "postgres://u@h/db", "mysql": "u@tcp(h:3306)/db", "oceanbase": "u@tcp(h:2881)/db",
	}
	dialects := map[string]dialect.Dialect{
		"postgres": postgres.Dialect{}, "mysql": mysql.Dialect{}, "oceanbase": oceanbase.Dialect{},
	}
	for _, driver := range providerregistry.Drivers() {
		d, ok := dialects[driver]
		if !ok {
			t.Fatalf("driver %q: no dialect in this test", driver)
		}
		for k := range d.Capabilities() {
			if strings.HasPrefix(string(k), "limit.") {
				t.Errorf("driver %q: dialect claims %s, a connection limit", driver, k)
			}
		}
		for _, pooler := range []string{"", providerregistry.PoolerTransaction} {
			limits, err := providerregistry.Limits(driver, dsns[driver], providerregistry.Options{Pooler: pooler})
			if err != nil {
				t.Fatalf("driver %q: %v", driver, err)
			}
			for k := range limits {
				if !strings.HasPrefix(string(k), "limit.") {
					t.Errorf("driver %q (pooler %q): limits claim %s, a dialect capability", driver, pooler, k)
				}
			}
		}
	}
}
