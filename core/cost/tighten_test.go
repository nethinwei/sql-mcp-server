package cost

import (
	"context"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/codegen"
)

type fixedExplainer struct{ plan Plan }

func (f fixedExplainer) Explain(context.Context, string, []any) (Plan, error) { return f.plan, nil }

// A caller's tightening makes the gate stricter, never looser.
func TestEstimateAppliesTheCallersTightening(t *testing.T) {
	t.Parallel()
	plan := Plan{ScanType: ScanFull, EstimatedRows: 10, StatsFresh: true}
	gate := Estimate{Explainer: fixedExplainer{plan}, Threshold: Threshold{}}
	read := codegen.Compiled{Kind: codegen.KindRead, SQL: "select 1"}
	if dec, err := gate.Check(context.Background(), read); err != nil || !dec.Allow {
		t.Fatalf("without tightening: %+v %v", dec, err)
	}
	ctx := WithTightening(context.Background(), Tightening{RejectFullScan: true})
	if dec, _ := gate.Check(ctx, read); dec.Allow {
		t.Fatal("a caller rejecting full scans was let through")
	}
	strict := Threshold{RejectFullScan: true, SoftScore: 80}
	if got := strict.Tighten(Tightening{SoftScore: 10}); !got.RejectFullScan || got.SoftScore != 80 {
		t.Fatalf("tightening loosened the gate: %+v", got)
	}
	if got := strict.Tighten(Tightening{HardScore: 60}); got.HardScore != 60 {
		t.Fatalf("hard score = %d", got.HardScore)
	}
}
