package mysql

import "testing"

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

func TestDialectExplainSQL(t *testing.T) {
	t.Parallel()
	q := "SELECT * FROM t"
	if got := (Dialect{}).ExplainSQL(q); got != "EXPLAIN FORMAT=JSON "+q {
		t.Errorf("ExplainSQL = %q", got)
	}
}

func TestDialectCapabilities(t *testing.T) {
	t.Parallel()
	caps := (Dialect{}).Capabilities()
	if caps.ExplainAccurate {
		t.Error("mysql estimate should not be accurate")
	}
	if !caps.SQLSafeUpdates {
		t.Error("mysql should support sql_safe_updates")
	}
	if caps.Returning {
		t.Error("mysql should not support RETURNING")
	}
}
