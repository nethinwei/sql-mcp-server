package hologres

import (
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/dialect"
)

func TestDialectQuoteIdent(t *testing.T) {
	t.Parallel()
	d := Dialect{}
	cases := []struct {
		in, want string
	}{
		{"user", `"user"`},
		{`a"b`, `"a""b"`},
	}
	for _, c := range cases {
		if got := d.QuoteIdent(c.in); got != c.want {
			t.Errorf("QuoteIdent(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDialectPlaceholder(t *testing.T) {
	t.Parallel()
	d := Dialect{}
	if got := d.Placeholder(0); got != "$1" {
		t.Errorf("Placeholder(0) = %q, want $1", got)
	}
	if got := d.Placeholder(2); got != "$3" {
		t.Errorf("Placeholder(2) = %q, want $3", got)
	}
}

func TestDialectExplainSQLIsTextOnly(t *testing.T) {
	t.Parallel()
	q := "SELECT * FROM t"
	if got := (Dialect{}).ExplainSQL(q); got != "EXPLAIN "+q {
		t.Errorf("ExplainSQL = %q", got)
	}
}

// TestDialectCapabilityProfile pins the Hologres capability profile: every
// weakening relative to PostgreSQL is deliberate and evidence-backed, so any
// change here must cite new evidence (see docs/provider-compatibility.md).
func TestDialectCapabilityProfile(t *testing.T) {
	t.Parallel()
	caps := (Dialect{}).Capabilities()
	want := dialect.Capabilities{
		Returning:        false,
		Savepoint:        false,
		KeysetCursor:     true,
		Transaction:      false,
		ExplainJSON:      false,
		ExplainCost:      false,
		ExplainAccurate:  false,
		StatementTimeout: true,
		ScanRowCap:       false,
		SQLSafeUpdates:   false,
		ResourceManager:  false,
	}
	if caps != want {
		t.Errorf("Capabilities = %+v, want %+v", caps, want)
	}
}
