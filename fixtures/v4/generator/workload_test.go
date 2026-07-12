package workload

import (
	"strings"
	"testing"
)

// TestGenerateDeterministic locks the reproducibility contract: the same
// Config renders byte-identical SQL, and a different seed changes values.
func TestGenerateDeterministic(t *testing.T) {
	a := strings.Join(Generate(DefaultConfig()).Statements(DialectPostgres), ";\n")
	b := strings.Join(Generate(DefaultConfig()).Statements(DialectPostgres), ";\n")
	if a != b {
		t.Fatal("same config produced different SQL")
	}
	other := DefaultConfig()
	other.Seed = 2
	if a == strings.Join(Generate(other).Statements(DialectPostgres), ";\n") {
		t.Fatal("different seed produced identical SQL")
	}
}

// TestScaleGrowsFactTables locks the scale contract: dimension tables stay
// fixed, fact tables multiply.
func TestScaleGrowsFactTables(t *testing.T) {
	base := Generate(DefaultConfig())
	cfg := DefaultConfig()
	cfg.Scale = 3
	scaled := Generate(cfg)
	if got, want := len(scaled.Table("wl_orders").Rows), 3*len(base.Table("wl_orders").Rows); got != want {
		t.Fatalf("scaled orders = %d, want %d", got, want)
	}
	if got, want := len(scaled.Table("wl_products").Rows), len(base.Table("wl_products").Rows); got != want {
		t.Fatalf("scaled products = %d, want %d (dimensions must not scale)", got, want)
	}
}

// TestLedgerTransactionsBalance locks double-entry integrity: every ledger
// transaction's entries sum to zero (credits minus debits) per currency.
func TestLedgerTransactionsBalance(t *testing.T) {
	d := Generate(DefaultConfig())
	entries := d.Table("wl_ledger_entries")
	txIdx := entries.ColumnIndex("transaction_id")
	dirIdx := entries.ColumnIndex("direction")
	amtIdx := entries.ColumnIndex("amount_minor")
	sums := map[int]int64{}
	for _, e := range entries.Rows {
		amt := e[amtIdx].(int64)
		if e[dirIdx].(string) == "debit" {
			amt = -amt
		}
		sums[e[txIdx].(int)] += amt
	}
	for tx, sum := range sums {
		if sum != 0 {
			t.Fatalf("ledger transaction %d does not balance: %d", tx, sum)
		}
	}
	if len(sums) == 0 {
		t.Fatal("no ledger transactions generated")
	}
}

// TestAnomalyTogglesRemovePatterns locks the anomaly-injection contract:
// disabling a toggle deterministically removes that pattern.
func TestAnomalyTogglesRemovePatterns(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Anomalies.DuplicateCallbacks = false
	cfg.Anomalies.DuplicateGiftEvents = false
	d := Generate(cfg)

	callbacks := d.Table("wl_channel_callbacks")
	extIdx := callbacks.ColumnIndex("external_event_id")
	seen := map[string]bool{}
	for _, row := range callbacks.Rows {
		id := row[extIdx].(string)
		if seen[id] {
			t.Fatalf("duplicate callback %s with DuplicateCallbacks disabled", id)
		}
		seen[id] = true
	}

	events := d.Table("wl_gift_events")
	dedupIdx := events.ColumnIndex("dedup_of")
	for _, row := range events.Rows {
		if row[dedupIdx] != nil {
			t.Fatal("duplicate gift event with DuplicateGiftEvents disabled")
		}
	}

	withAnomalies := Generate(DefaultConfig())
	dupes := 0
	for _, row := range withAnomalies.Table("wl_gift_events").Rows {
		if row[dedupIdx] != nil {
			dupes++
		}
	}
	if dupes == 0 {
		t.Fatal("reference profile generated no duplicate gift events")
	}
}

// TestDialectRendering smoke-checks all three dialects render and differ
// only where types differ.
func TestDialectRendering(t *testing.T) {
	d := Generate(DefaultConfig())
	pg := d.Statements(DialectPostgres)
	my := d.Statements(DialectMySQL)
	ob := d.Statements(DialectOceanBase)
	if len(pg) != len(my) || len(my) != len(ob) {
		t.Fatalf("statement counts differ: pg=%d mysql=%d ob=%d", len(pg), len(my), len(ob))
	}
	joinedMy := strings.Join(my, ";\n")
	if strings.Join(ob, ";\n") != joinedMy {
		t.Fatal("mysql and oceanbase renderings must be identical")
	}
	if !strings.Contains(joinedMy, "datetime") || strings.Contains(joinedMy, " timestamp") {
		t.Fatal("mysql rendering must map ColDateTime to datetime")
	}
}
