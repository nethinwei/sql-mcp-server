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
		Threshold: Threshold{RejectFullScan: true, HardScore: 40, SoftScore: 60},
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
		Threshold: Threshold{SoftScore: 70, HardScore: 30},
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
		Threshold: Threshold{SoftScore: 70, HardScore: 30},
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

func TestStaticRuleTemplates(t *testing.T) {
	t.Parallel()
	sr := StaticRule{
		AllowTemplates:  []string{"SELECT 1"},
		RejectTemplates: []string{"SELECT bad"},
	}
	// rejected template
	d, _ := sr.Check(context.Background(), codegen.Compiled{SQL: "SELECT bad"})
	if d.Allow {
		t.Fatal("rejected template should deny")
	}
	// allowed template bypasses
	d, _ = sr.Check(context.Background(), codegen.Compiled{SQL: "SELECT 1"})
	if !d.Allow || !d.Bypass {
		t.Fatalf("allowed template should bypass, got %+v", d)
	}
	// unknown template passes through
	d, _ = sr.Check(context.Background(), codegen.Compiled{SQL: "SELECT other"})
	if !d.Allow || d.Bypass {
		t.Fatalf("unknown template should pass without bypass, got %+v", d)
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
		Threshold{SoftScore: 70, HardScore: 30, MaxRows: 1000}, nil)
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

func TestWriteGuard(t *testing.T) {
	t.Parallel()
	var wg WriteGuard
	if d, _ := wg.Check(context.Background(), codegen.Compiled{ReadOnly: true}); !d.Allow {
		t.Fatal("read should pass the write guard")
	}
	if d, _ := wg.Check(context.Background(), codegen.Compiled{IsPKPoint: true}); !d.Allow {
		t.Fatal("PK point write should pass")
	}
	d, _ := wg.Check(context.Background(), codegen.Compiled{SQL: "UPDATE t SET x=1 WHERE status='a'"})
	if d.Allow || len(d.Hints) == 0 {
		t.Fatalf("non-PK write must be hard-rejected with hints, got %+v", d)
	}
}

func TestNewGateFromCapabilitiesWriteGuardMySQL(t *testing.T) {
	t.Parallel()
	// MySQL has no trustworthy Estimate; WriteGuard must still block non-PK
	// writes deterministically.
	caps := dialect.MySQL{}.Capabilities()
	g := NewGateFromCapabilities(caps, nil, Threshold{RequirePKForWrite: true, MaxRows: 1000}, nil)
	d, err := g.Check(context.Background(), codegen.Compiled{SQL: "UPDATE t SET x=1", IsPKPoint: false})
	if err != nil {
		t.Fatal(err)
	}
	if d.Allow {
		t.Fatal("non-PK write must be rejected by WriteGuard on MySQL")
	}
	dpk, err := g.Check(context.Background(), codegen.Compiled{SQL: "UPDATE t SET x=1 WHERE id=1", IsPKPoint: true})
	if err != nil {
		t.Fatal(err)
	}
	if !dpk.Allow {
		t.Fatal("PK point write should pass")
	}
}
