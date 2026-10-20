package mysql

import (
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
)

func TestLimits(t *testing.T) {
	t.Parallel()
	pooled := providerregistry.Options{Pooler: providerregistry.PoolerTransaction}
	tests := []struct {
		name                 string
		dsn                  string
		opts                 providerregistry.Options
		variable             string
		read, write, unkeyed dialect.Strength
	}{
		{"injected", "u:p@tcp(h)/db", providerregistry.Options{}, "",
			dialect.Enforced, dialect.Enforced, dialect.Enforced},
		{"DSN timeout", "u:p@tcp(h)/db?max_execution_time=0", providerregistry.Options{}, "",
			dialect.BestEffort, dialect.Enforced, dialect.Enforced},
		{"DSN safe updates off", "u:p@tcp(h)/db?sql_safe_updates=0", providerregistry.Options{}, "",
			dialect.Enforced, dialect.Enforced, dialect.Unsupported},
		{"DSN names in upper case", "u:p@tcp(h)/db?SQL_SAFE_UPDATES=0&Max_Execution_Time=0", providerregistry.Options{},
			"", dialect.BestEffort, dialect.Enforced, dialect.Unsupported},
		{"DSN safe updates on", "u:p@tcp(h)/db?sql_safe_updates=ON", providerregistry.Options{}, "",
			dialect.Enforced, dialect.Enforced, dialect.Enforced},
		{"DSN safe updates not understood", "u:p@tcp(h)/db?sql_safe_updates=2", providerregistry.Options{}, "",
			dialect.Enforced, dialect.Enforced, dialect.BestEffort},
		{"pooler", "u:p@tcp(h)/db", pooled, "",
			dialect.BestEffort, dialect.Unsupported, dialect.BestEffort},
		{"oceanbase injected", "u:p@tcp(h)/db", providerregistry.Options{}, "ob_query_timeout",
			dialect.Enforced, dialect.Enforced, dialect.Enforced},
		{"oceanbase DSN timeout", "u:p@tcp(h)/db?ob_query_timeout=0", providerregistry.Options{}, "ob_query_timeout",
			dialect.BestEffort, dialect.BestEffort, dialect.Enforced},
	}
	for _, test := range tests {
		p, err := LimitsFor(test.dsn, test.opts, test.variable)
		if err != nil {
			t.Fatal(err)
		}
		got := []dialect.Strength{
			p.Claim(dialect.TimeoutRead).Strength, p.Claim(dialect.TimeoutWrite).Strength,
			p.Claim(dialect.UnkeyedWrite).Strength,
		}
		want := []dialect.Strength{test.read, test.write, test.unkeyed}
		if got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
			t.Errorf("%s: read/write/unkeyed = %v, want %v", test.name, got, want)
		}
		for _, k := range []dialect.Key{dialect.TimeoutRead, dialect.TimeoutWrite, dialect.UnkeyedWrite} {
			if p.Claim(k).Reason.Text == "" {
				t.Errorf("%s: %s has no reason", test.name, k)
			}
		}
	}
}

// A DSN setting a variable twice, in different cases, is refused: which one
// the server applies last is unknown.
func TestLimitsRefuseAVariableSetTwice(t *testing.T) {
	t.Parallel()
	if _, err := Limits("u:p@tcp(h)/db?sql_safe_updates=1&SQL_SAFE_UPDATES=0", providerregistry.Options{}); err == nil {
		t.Fatal("a variable set twice was accepted")
	}
}
