package dialect

import (
	"errors"
	"testing"
)

func TestQuoteIdent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		d    Dialect
		in   string
		want string
	}{
		{Postgres{}, "user", `"user"`},
		{Postgres{}, `a"b`, `"a""b"`},
		{MySQL{}, "user", "`user`"},
		{MySQL{}, "a`b", "`a``b`"},
		{OceanBase{}, "user", "`user`"},
	}
	for _, c := range cases {
		if got := c.d.QuoteIdent(c.in); got != c.want {
			t.Errorf("%s QuoteIdent(%q) = %q, want %q", c.d.Name(), c.in, got, c.want)
		}
	}
}

func TestPlaceholder(t *testing.T) {
	t.Parallel()
	pg, my, ob := Postgres{}, MySQL{}, OceanBase{}
	if got := pg.Placeholder(0); got != "$1" {
		t.Errorf("postgres Placeholder(0) = %q, want $1", got)
	}
	if got := pg.Placeholder(2); got != "$3" {
		t.Errorf("postgres Placeholder(2) = %q, want $3", got)
	}
	if got := my.Placeholder(0); got != "?" {
		t.Errorf("mysql Placeholder(0) = %q, want ?", got)
	}
	if got := ob.Placeholder(5); got != "?" {
		t.Errorf("oceanbase Placeholder(5) = %q, want ?", got)
	}
}

func TestExplainSQL(t *testing.T) {
	t.Parallel()
	pg, my, ob := Postgres{}, MySQL{}, OceanBase{}
	q := "SELECT * FROM t"
	if got := pg.ExplainSQL(q); got != "EXPLAIN (FORMAT JSON) "+q {
		t.Errorf("postgres ExplainSQL = %q", got)
	}
	if got := my.ExplainSQL(q); got != "EXPLAIN FORMAT=JSON "+q {
		t.Errorf("mysql ExplainSQL = %q", got)
	}
	if got := ob.ExplainSQL(q); got != "EXPLAIN FORMAT=JSON "+q {
		t.Errorf("oceanbase ExplainSQL = %q", got)
	}
}

func TestCapabilities(t *testing.T) {
	t.Parallel()
	pg, my, ob := Postgres{}, MySQL{}, OceanBase{}
	if !pg.Capabilities().ExplainAccurate {
		t.Error("postgres estimate should be accurate")
	}
	if my.Capabilities().ExplainAccurate {
		t.Error("mysql estimate should not be accurate")
	}
	if !my.Capabilities().SQLSafeUpdates {
		t.Error("mysql should support sql_safe_updates")
	}
	if !ob.Capabilities().ResourceManager {
		t.Error("oceanbase should have resource manager")
	}
	if !ob.Capabilities().ScanRowCap {
		t.Error("oceanbase should have scan row cap")
	}
	if !pg.Capabilities().Returning {
		t.Error("postgres should support RETURNING")
	}
	if my.Capabilities().Returning {
		t.Error("mysql should not support RETURNING")
	}
}

func TestNew(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"postgres", "mysql", "oceanbase"} {
		d, err := New(name)
		if err != nil || d.Name() != name {
			t.Fatalf("New(%q) = %v, %v", name, d, err)
		}
	}
	if _, err := New("oracle"); !errors.Is(err, ErrUnknownDialect) {
		t.Fatalf("got %v, want ErrUnknownDialect", err)
	}
}
