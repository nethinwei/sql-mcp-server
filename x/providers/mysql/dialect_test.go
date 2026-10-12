package mysql

import (
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/dialect"
)

func TestDialectQuoteIdent(t *testing.T) {
	t.Parallel()
	d := Dialect{}
	if got := d.QuoteIdent("user"); got != "`user`" {
		t.Errorf("QuoteIdent = %q", got)
	}
	if got := d.QuoteIdent("a`b"); got != "`a``b`" {
		t.Errorf("QuoteIdent = %q", got)
	}
}

func TestDialectPlaceholder(t *testing.T) {
	t.Parallel()
	if got := (Dialect{}).Placeholder(0); got != "?" {
		t.Errorf("Placeholder(0) = %q, want ?", got)
	}
}

func TestDialectCapabilities(t *testing.T) {
	t.Parallel()
	caps := (Dialect{}).Capabilities()
	if caps.Enforced(dialect.ExplainAccurate) {
		t.Error("mysql estimate should not be accurate")
	}
	if caps.Has(dialect.Returning) {
		t.Error("mysql should not support RETURNING")
	}
}
