package conformance

import (
	"math/big"
	"reflect"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/relalg"
	"github.com/nethinwei/sql-mcp-server/core/relalg/interp"
)

// TestInterpreterCoversWholeCorpus guards the corpus itself: every case
// (fixed and generated) must be evaluable by the oracle, so an integration
// failure is always a real divergence, never a corpus bug.
func TestInterpreterCoversWholeCorpus(t *testing.T) {
	fixture := Fixture()
	cases := Cases("")
	if len(cases) < 20+propertyCases {
		t.Fatalf("corpus unexpectedly small: %d cases", len(cases))
	}
	for _, c := range cases {
		if _, err := interp.Eval(fixture, c.Expr); err != nil {
			t.Errorf("case %s not evaluable by the oracle: %v", c.Name, err)
		}
	}
}

// TestCorpusIsDeterministic locks the fixed seed: the corpus is versioned,
// not fuzzed, so both sides of the differential always see the same cases.
func TestCorpusIsDeterministic(t *testing.T) {
	if !reflect.DeepEqual(Cases("s"), Cases("s")) {
		t.Fatal("Cases must be deterministic across invocations")
	}
}

// TestFixtureHandChecked pins a few oracle results by hand so the oracle and
// fixture cannot drift together unnoticed.
func TestFixtureHandChecked(t *testing.T) {
	fixture := Fixture()
	res, err := interp.Eval(fixture, relalg.Aggregate{
		Input: relalg.Scan{Relation: relalg.RelationRef{Name: TableName}},
		Aggregates: []relalg.AggCall{
			{Func: relalg.AggCount},
			{Func: relalg.AggCount, Field: "val"},
			{Func: relalg.AggSum, Field: "val"},
			{Func: relalg.AggAvg, Field: "val"},
			{Func: relalg.AggAvg, Field: "price"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	row := res.Rows[0]
	if row[0] != int64(12) || row[1] != int64(10) {
		t.Fatalf("count(*)=%v count(val)=%v, want 12 and 10", row[0], row[1])
	}
	if row[2].(*big.Rat).Cmp(big.NewRat(41, 1)) != 0 {
		t.Fatalf("sum(val)=%v, want 41", row[2])
	}
	// Both averages terminate within two decimal digits by fixture design
	// (the avg-precision deviation documented in the spec).
	if row[3].(*big.Rat).Cmp(big.NewRat(41, 10)) != 0 {
		t.Fatalf("avg(val)=%v, want 4.1", row[3])
	}
	if row[4].(*big.Rat).Cmp(big.NewRat(5, 2)) != 0 {
		t.Fatalf("avg(price)=%v, want 2.5", row[4])
	}
}

// TestGroupedAveragesTerminate re-checks the fixture invariant that every
// grouped average in the corpus terminates within two decimal digits.
func TestGroupedAveragesTerminate(t *testing.T) {
	res, err := interp.Eval(Fixture(), relalg.Aggregate{
		Input:      relalg.Scan{Relation: relalg.RelationRef{Name: TableName}},
		GroupBy:    []string{"cat"},
		Aggregates: []relalg.AggCall{{Func: relalg.AggAvg, Field: "val"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	hundred := big.NewRat(100, 1)
	for _, row := range res.Rows {
		avg := row[1].(*big.Rat)
		scaled := new(big.Rat).Mul(avg, hundred)
		if !scaled.IsInt() {
			t.Fatalf("avg for group %v = %v does not terminate within 2 decimals", row[0], avg)
		}
	}
}

// TestNormalizationBridgesDriverTypes checks that provider-side raw values
// ([]byte decimals, exact strings) and oracle-side values (int64, *big.Rat)
// normalize to identical keys.
func TestNormalizationBridgesDriverTypes(t *testing.T) {
	pairs := [][2]any{
		{int64(41), []byte("41")},
		{big.NewRat(41, 10), "4.1000"},
		{big.NewRat(5, 2), []byte("2.50")},
		{nil, nil},
		{"alpha", []byte("alpha")},
	}
	for _, p := range pairs {
		a, err := canonicalKey(p[0])
		if err != nil {
			t.Fatal(err)
		}
		b, err := canonicalKey(p[1])
		if err != nil {
			t.Fatal(err)
		}
		if a != b {
			t.Fatalf("canonicalKey(%v)=%q != canonicalKey(%v)=%q", p[0], a, p[1], b)
		}
	}
}
