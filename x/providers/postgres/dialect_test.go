package postgres

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

func TestDialectCapabilities(t *testing.T) {
	t.Parallel()
	caps := (Dialect{}).Capabilities()
	if !caps.Enforced(dialect.ExplainAccurate) {
		t.Error("postgres estimate should be accurate")
	}
	if !caps.Enforced(dialect.Returning) {
		t.Error("postgres should support RETURNING")
	}
}
