package postgres

import (
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
)

// The cancel request keeps both timeouts enforced however statement_timeout
// is set; the reason says which mechanism holds them.
func TestLimits(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, dsn, reason string
		opts              providerregistry.Options
	}{
		{"injected", "postgres://u@h/db", "statement_timeout just under cost.queryTimeout", providerregistry.Options{}},
		{"DSN timeout", "postgres://u@h/db?statement_timeout=0", "the DSN sets", providerregistry.Options{}},
		{"pooler", "postgres://u@h/db", "transaction pooler",
			providerregistry.Options{Pooler: providerregistry.PoolerTransaction}},
	}
	for _, test := range tests {
		p, err := Limits(test.dsn, test.opts)
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range []dialect.Key{dialect.TimeoutRead, dialect.TimeoutWrite} {
			c := p.Claim(k)
			if c.Strength != dialect.Enforced || !strings.Contains(c.Reason, test.reason) {
				t.Errorf("%s: %s = %+v", test.name, k, c)
			}
		}
		if p.Has(dialect.UnkeyedWrite) {
			t.Errorf("%s: PostgreSQL refuses no unkeyed write itself", test.name)
		}
	}
}
