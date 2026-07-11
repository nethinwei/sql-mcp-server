package postgres

import "testing"

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

func TestDialectExplainSQL(t *testing.T) {
	t.Parallel()
	q := "SELECT * FROM t"
	if got := (Dialect{}).ExplainSQL(q); got != "EXPLAIN (FORMAT JSON) "+q {
		t.Errorf("ExplainSQL = %q", got)
	}
}

func TestDialectCapabilities(t *testing.T) {
	t.Parallel()
	caps := (Dialect{}).Capabilities()
	if !caps.ExplainAccurate {
		t.Error("postgres estimate should be accurate")
	}
	if !caps.Returning {
		t.Error("postgres should support RETURNING")
	}
	if caps.SQLSafeUpdates {
		t.Error("postgres should not use sql_safe_updates")
	}
}
