package cost

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/codegen"
	"github.com/nethinwei/sql-mcp-server/dialect"
)

func TestScorePlan(t *testing.T) {
	t.Parallel()
	if s := ScorePlan(Plan{ScanType: ScanFull}); s.Value >= 50 {
		t.Fatalf("full scan score %d too high", s.Value)
	}
	if s := ScorePlan(Plan{ScanType: ScanPoint, StatsFresh: true}); s.Value != 100 {
		t.Fatalf("point score = %d, want 100", s.Value)
	}
	if s := ScorePlan(Plan{ScanType: ScanUnknown}); s.Value != 35 {
		t.Fatalf("unknown+stale score = %d, want 35", s.Value)
	}
}

func TestEstimateHardRejectFullScan(t *testing.T) {
	t.Parallel()
	est := Estimate{
		Explainer: FakeExplainer{Plan: Plan{ScanType: ScanFull, StatsFresh: true}},
		Threshold: Threshold{RejectFullScan: true, HardScore: 90, SoftScore: 50},
	}
	d, err := est.Check(context.Background(), codegen.Compiled{SQL: "SELECT * FROM t"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Allow || d.Soft {
		t.Fatalf("expected hard reject, got %+v", d)
	}
}

func TestEstimateSoftReject(t *testing.T) {
	t.Parallel()
	est := Estimate{
		Explainer: FakeExplainer{Plan: Plan{ScanType: ScanSeq, StatsFresh: true}}, // score 60
		Threshold: Threshold{SoftScore: 50, HardScore: 80},
	}
	d, _ := est.Check(context.Background(), codegen.Compiled{})
	if !d.Soft {
		t.Fatalf("expected soft reject, got %+v", d)
	}
}

func TestEstimateExplainFailureDegrades(t *testing.T) {
	t.Parallel()
	est := Estimate{
		Explainer: FakeExplainer{Err: errors.New("explain failed")},
		Threshold: Threshold{RequireKnownScan: true},
	}
	d, _ := est.Check(context.Background(), codegen.Compiled{})
	if d.Allow {
		t.Fatal("unknown scan with RequireKnownScan should reject")
	}
}

func TestEstimatePass(t *testing.T) {
	t.Parallel()
	est := Estimate{
		Explainer: FakeExplainer{Plan: Plan{ScanType: ScanIndex, StatsFresh: true}}, // 85
		Threshold: Threshold{SoftScore: 90, HardScore: 95},
	}
	d, _ := est.Check(context.Background(), codegen.Compiled{})
	if !d.Allow {
		t.Fatalf("expected pass, got %+v", d)
	}
}

func TestEnforceCapWrapsLimit(t *testing.T) {
	t.Parallel()
	ec := EnforceCap{HardRows: 100}
	d, _ := ec.Check(context.Background(), codegen.Compiled{SQL: "SELECT * FROM t", ReadOnly: true})
	if d.Rewritten == nil || !strings.Contains(d.Rewritten.SQL, "LIMIT 100") {
		t.Fatalf("expected wrapped LIMIT, got %+v", d)
	}
}

func TestEnforceCapSkipsWrites(t *testing.T) {
	t.Parallel()
	ec := EnforceCap{HardRows: 100}
	d, _ := ec.Check(context.Background(), codegen.Compiled{SQL: "UPDATE t SET x=1", ReadOnly: false})
	if d.Rewritten != nil {
		t.Fatal("write should not be wrapped")
	}
}

func TestStaticRulePKWhitelist(t *testing.T) {
	t.Parallel()
	sr := StaticRule{PKWhitelist: true}
	d, _ := sr.Check(context.Background(), codegen.Compiled{IsPKPoint: true})
	if !d.Allow || !d.Bypass || d.Plan == nil {
		t.Fatalf("PK point should be whitelisted with bypass, got %+v", d)
	}
}

func TestChainGateShortCircuits(t *testing.T) {
	t.Parallel()
	g := NewGate(
		StaticRule{PKWhitelist: false},
		Estimate{
			Explainer: FakeExplainer{Plan: Plan{ScanType: ScanFull, StatsFresh: true}},
			Threshold: Threshold{RejectFullScan: true},
		},
	)
	d, _ := g.Check(context.Background(), codegen.Compiled{IsPKPoint: false})
	if d.Allow {
		t.Fatal("expected reject")
	}
}

func TestChainGatePKWhitelistSkipsEstimate(t *testing.T) {
	t.Parallel()
	g := NewGate(
		StaticRule{PKWhitelist: true},
		Estimate{
			Explainer: FakeExplainer{Plan: Plan{ScanType: ScanFull}},
			Threshold: Threshold{RejectFullScan: true},
		},
	)
	d, _ := g.Check(context.Background(), codegen.Compiled{IsPKPoint: true})
	if !d.Allow {
		t.Fatalf("PK whitelist should bypass estimate, got %+v", d)
	}
}

func TestCostExceededErrorUnwrap(t *testing.T) {
	t.Parallel()
	e := &ExceededError{Plan: Plan{ScanType: ScanFull}}
	if !errors.Is(e, ErrCostExceeded) {
		t.Fatal("errors.Is(ErrCostExceeded) failed")
	}
}

func TestNewGateFromCapabilities(t *testing.T) {
	t.Parallel()
	caps := dialect.Postgres{}.Capabilities()
	g := NewGateFromCapabilities(caps,
		FakeExplainer{Plan: Plan{ScanType: ScanIndex, StatsFresh: true}},
		Threshold{SoftScore: 90, HardScore: 95, MaxRows: 1000}, nil)
	d, _ := g.Check(context.Background(), codegen.Compiled{ReadOnly: true})
	if !d.Allow {
		t.Fatalf("expected pass, got %+v", d)
	}
	if d.Rewritten == nil {
		t.Fatal("expected EnforceCap to wrap with LIMIT")
	}
}

func TestNewGateFromCapabilitiesSQLiteSkipsEstimate(t *testing.T) {
	t.Parallel()
	// SQLite has no ExplainCost, so Estimate must not be assembled.
	caps := dialect.Capabilities{ExplainCost: false, ExplainAccurate: false}
	g := NewGateFromCapabilities(caps, nil, Threshold{MaxRows: 100}, nil)
	names := []string{}
	for _, l := range g.Layers() {
		names = append(names, l.Name())
	}
	if strings.Contains(strings.Join(names, ","), "estimate") {
		t.Fatalf("SQLite should not assemble Estimate layer: %v", names)
	}
}
