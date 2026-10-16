package cost

import (
	"context"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/codegen"
)

// An index range reading ten million rows is refused by the scanned-rows
// limit although its access path scores well, as the full scan of a large
// table is whatever its output.
func TestEstimateRefusesPlansReadingTooManyRows(t *testing.T) {
	t.Parallel()
	read := codegen.Compiled{Kind: codegen.KindRead, SQL: "select 1"}
	th := Threshold{SoftScore: 60, HardScore: 40, MaxScannedRows: 100_000}
	for name, tc := range map[string]struct {
		plan  Plan
		allow bool
	}{
		"index range over 11M rows": {Plan{ScanType: ScanIndex, EstimatedRows: 11_482_410, StatsFresh: true}, false},
		"index range within limit":  {Plan{ScanType: ScanIndex, EstimatedRows: 90_000, StatsFresh: true}, true},
		"full scan of a large table outputting one row": {
			Plan{ScanType: ScanFull, EstimatedRows: 1, TableRows: 500_000, TableSized: true, StatsFresh: true}, false},
		"full scan of an unsized table": {Plan{ScanType: ScanFull, EstimatedRows: 1, StatsFresh: true}, false},
	} {
		dec, err := Estimate{Explainer: fixedExplainer{tc.plan}, Threshold: th}.Check(context.Background(), read)
		if err != nil {
			t.Fatal(name, err)
		}
		// The unsized full scan scores 20: refused by the hard score, not the limit.
		if dec.Allow != tc.allow {
			t.Errorf("%s: allow = %v, want %v (%+v)", name, dec.Allow, tc.allow, dec)
		}
		if !tc.allow && tc.plan.ScannedRows() > th.MaxScannedRows && !strings.Contains(strings.Join(dec.Hints, "\n"),
			"over the limit of 100000") {
			t.Errorf("%s: hints do not give the numbers: %v", name, dec.Hints)
		}
	}
	unlimited := Estimate{Explainer: fixedExplainer{Plan{ScanType: ScanIndex, EstimatedRows: 900_000,
		StatsFresh: true}}, Threshold: Threshold{SoftScore: 40, HardScore: 20}}
	if dec, _ := unlimited.Check(context.Background(), read); !dec.Allow {
		t.Fatalf("0 is unlimited: %+v", dec)
	}
}

// The row estimate lowers the score by its order of magnitude.
func TestScorePlanPenalizesRowsByMagnitude(t *testing.T) {
	t.Parallel()
	want := map[int64]int{10_000: 85, 50_000: 70, 500_000: 60, 5_000_000: 50, 50_000_000: 40}
	for rows, score := range want {
		if got := ScorePlan(Plan{ScanType: ScanIndex, EstimatedRows: rows, StatsFresh: true}).Value; got != score {
			t.Errorf("%d rows: score %d, want %d", rows, got, score)
		}
	}
}

// A caller's scanned-rows limit only lowers the global one; it sets one
// where the global is unlimited.
func TestTightenScannedRows(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ global, own, want int64 }{
		{100_000, 0, 100_000}, {100_000, 50_000, 50_000}, {100_000, 500_000, 100_000}, {0, 50_000, 50_000},
	} {
		if got := (Threshold{MaxScannedRows: tc.global}).Tighten(Tightening{MaxScannedRows: tc.own}); got.MaxScannedRows !=
			tc.want {
			t.Errorf("global %d, own %d: %d, want %d", tc.global, tc.own, got.MaxScannedRows, tc.want)
		}
	}
}
