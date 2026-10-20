package postgres

import (
	"strings"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
)

// The cancel request keeps both timeouts enforced however statement_timeout
// is set on a direct connection; the reason says which mechanism holds them.
// Behind a transaction pooler they are best effort: the pooler drops
// statement_timeout and may not forward the cancel request.
func TestLimits(t *testing.T) {
	t.Parallel()
	timed := providerregistry.Options{Timeout: time.Second}
	tests := []struct {
		name, dsn, reason string
		opts              providerregistry.Options
		want              dialect.Strength
	}{
		{"injected", "postgres://u@h/db", "statement_timeout just under cost.queryTimeout",
			timed, dialect.Enforced},
		{"DSN timeout", "postgres://u@h/db?statement_timeout=0", "the DSN sets", timed,
			dialect.Enforced},
		{"DSN timeout in upper case", "postgres://u@h/db?Statement_Timeout=0", "the DSN sets",
			timed, dialect.Enforced},
		{"pooler", "postgres://u@h/db", "transaction pooler",
			providerregistry.Options{Timeout: time.Second, Pooler: providerregistry.PoolerTransaction},
			dialect.BestEffort},
		{"no timeout", "postgres://u@h/db", "cost.queryTimeout is 0", providerregistry.Options{},
			dialect.Unsupported},
	}
	for _, test := range tests {
		p, err := Limits(test.dsn, test.opts)
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range []dialect.Key{dialect.TimeoutRead, dialect.TimeoutWrite} {
			c := p.Claim(k)
			if c.Strength != test.want || !strings.Contains(c.Reason.Text, test.reason) {
				t.Errorf("%s: %s = %+v", test.name, k, c)
			}
		}
		if p.Has(dialect.UnkeyedWrite) {
			t.Errorf("%s: PostgreSQL refuses no unkeyed write itself", test.name)
		}
	}
}

// Without a timeout (cost.queryTimeout 0) the provider opens, sets none, and
// says so.
func TestOpenWithoutATimeout(t *testing.T) {
	t.Parallel()
	p, err := Open("postgres://u@h/db", providerregistry.Options{Lazy: true})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if c := p.Profile().Claim(dialect.TimeoutRead); c.Strength != dialect.Unsupported {
		t.Fatalf("timeout = %+v", c)
	}
	if _, err := Open("postgres://u@h/db", providerregistry.Options{Timeout: -1, Lazy: true}); err == nil {
		t.Fatal("a negative timeout was accepted")
	}
}
