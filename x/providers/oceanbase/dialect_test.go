package oceanbase

import "testing"

func TestDialectQuoteIdent(t *testing.T) {
	t.Parallel()
	if got := (Dialect{}).QuoteIdent("user"); got != "`user`" {
		t.Errorf("QuoteIdent = %q", got)
	}
}

func TestDialectPlaceholder(t *testing.T) {
	t.Parallel()
	if got := (Dialect{}).Placeholder(5); got != "?" {
		t.Errorf("Placeholder(5) = %q, want ?", got)
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
		t.Error("oceanbase estimate should not be accurate")
	}
	if !caps.ResourceManager {
		t.Error("oceanbase should have resource manager")
	}
	if !caps.ScanRowCap {
		t.Error("oceanbase should have scan row cap")
	}
}
