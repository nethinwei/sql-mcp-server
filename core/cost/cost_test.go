package cost

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/codegen"
	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
	"github.com/nethinwei/sql-mcp-server/internal/testdialect"
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

func TestEstimateFailClosed(t *testing.T) {
	t.Parallel()
	for _, est := range []Estimate{
		NewEstimate(FakeExplainer{Err: errors.New("explain failed")}, Threshold{}, nil, WithFailClosed()),
		NewEstimate(FakeExplainer{Plan: Plan{ScanType: ScanUnknown}}, Threshold{}, nil, WithFailClosed()),
	} {
		d, err := est.Check(context.Background(), codegen.Compiled{Kind: codegen.KindRead})
		if err != nil {
			t.Fatal(err)
		}
		if d.Allow || d.Soft {
			t.Fatalf("fail-closed estimate must hard reject: %+v", d)
		}
	}
	defaultEstimate := NewEstimate(
		FakeExplainer{Err: errors.New("explain failed")}, Threshold{}, nil,
	)
	if d, _ := defaultEstimate.Check(context.Background(), codegen.Compiled{Kind: codegen.KindRead}); !d.Allow {
		t.Fatalf("default estimate must retain fail-open compatibility: %+v", d)
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

func TestEnforceCapHandlesKindsWithoutWrappingCall(t *testing.T) {
	t.Parallel()
	ec := EnforceCap{HardRows: 10}
	for _, kind := range []codegen.Kind{codegen.KindRead, codegen.KindAggregate} {
		d, _ := ec.Check(context.Background(), codegen.Compiled{Kind: kind, SQL: "SELECT 1"})
		if d.Rewritten == nil {
			t.Fatalf("%s should be capped", kind)
		}
	}
	d, _ := ec.Check(context.Background(), codegen.Compiled{
		Kind: codegen.KindCall, SQL: "CALL p()", ReadOnly: true,
	})
	if d.Rewritten != nil {
		t.Fatalf("CALL must not receive a SELECT wrapper: %+v", d)
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

func TestStaticRuleRejectPrecedesPKWhitelist(t *testing.T) {
	t.Parallel()
	sr := StaticRule{PKWhitelist: true, LegacyExactSQL: true, RejectTemplates: []string{"SELECT blocked"}}
	d, err := sr.Check(context.Background(), codegen.Compiled{SQL: "SELECT blocked", IsPKPoint: true})
	if err != nil {
		t.Fatal(err)
	}
	if d.Allow || d.Bypass {
		t.Fatalf("reject baseline was bypassed by PK whitelist: %+v", d)
	}
}

func TestStaticRuleTemplates(t *testing.T) {
	t.Parallel()
	sr := StaticRule{
		LegacyExactSQL:  true,
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

func TestStaticRuleExactSQLIsDatasourceScoped(t *testing.T) {
	compiled := codegen.Compiled{SQL: "SELECT 1"}
	primary := StaticRule{
		Datasource: "primary", AllowTemplates: []string{"primary:SELECT 1"},
	}
	d, _ := primary.Check(context.Background(), compiled)
	if !d.Bypass {
		t.Fatal("datasource-qualified exact SQL did not match its source")
	}
	replica := StaticRule{
		Datasource: "replica", AllowTemplates: []string{"primary:SELECT 1", "SELECT 1"},
	}
	d, _ = replica.Check(context.Background(), compiled)
	if d.Bypass {
		t.Fatal("multi-datasource rule accepted another source or bare exact SQL")
	}
	replica.LegacyExactSQL = true
	d, _ = replica.Check(context.Background(), compiled)
	if !d.Bypass {
		t.Fatal("single-datasource compatibility did not accept bare exact SQL")
	}
}

func TestValidateTemplateScopes(t *testing.T) {
	t.Parallel()
	datasources := []string{"replica", "primary"}
	for _, tc := range []struct {
		name   string
		allow  []string
		reject []string
		want   string
	}{
		{name: "fingerprints", allow: []string{"fp:v2:abc"}, reject: []string{"fp:v2:def"}},
		{name: "scoped exact SQL", allow: []string{"primary:SELECT 1"}, reject: []string{"replica:SELECT bad"}},
		{name: "bare allow", allow: []string{"SELECT 1"}, want: "allowTemplates"},
		{name: "bare reject", reject: []string{"SELECT bad"}, want: "rejectTemplates"},
		{name: "unknown datasource", reject: []string{"archive:SELECT bad"}, want: "rejectTemplates"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTemplateScopes(datasources, tc.allow, tc.reject)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("ValidateTemplateScopes() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) ||
				!strings.Contains(err.Error(), "fp:v2:<sha256>") ||
				!strings.Contains(err.Error(), "primary:") {
				t.Fatalf("ValidateTemplateScopes() error = %v, want migration guidance for %s", err, tc.want)
			}
		})
	}
	if err := ValidateTemplateScopes([]string{"primary"}, []string{"SELECT 1"}, []string{"SELECT bad"}); err != nil {
		t.Fatalf("single datasource compatibility error = %v", err)
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

func TestChainGateBypassStillRunsEnforcement(t *testing.T) {
	t.Parallel()
	g := NewGate(
		StaticRule{PKWhitelist: true},
		Estimate{
			Explainer: FakeExplainer{Plan: Plan{ScanType: ScanFull}},
			Threshold: Threshold{RejectFullScan: true},
		},
		EnforceCap{HardRows: 25},
	)
	d, err := g.Check(context.Background(), codegen.Compiled{
		Kind: codegen.KindRead, SQL: "SELECT * FROM t", ReadOnly: true, IsPKPoint: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Allow || d.Rewritten == nil || !strings.Contains(d.Rewritten.SQL, "LIMIT 25") {
		t.Fatalf("bypass must skip Estimate but retain EnforceCap: %+v", d)
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
	caps := testdialect.Postgres{}.Capabilities()
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

func TestCallGuardDefaultsDenyAndAllowsReviewedTemplate(t *testing.T) {
	t.Parallel()
	call := codegen.Compiled{Kind: codegen.KindCall, SQL: "CALL p()"}
	if d, _ := (CallGuard{}).Check(context.Background(), call); d.Allow {
		t.Fatal("CALL should be denied by default")
	}
	guard := CallGuard{LegacyExactSQL: true, AllowTemplates: []string{"CALL p()"}}
	if d, _ := guard.Check(context.Background(), call); !d.Allow {
		t.Fatalf("reviewed CALL should be allowed: %+v", d)
	}
}

func TestAggregateGuardRequiresPredicate(t *testing.T) {
	t.Parallel()
	guard := AggregateGuard{RequirePredicate: true}
	unfiltered := codegen.Compiled{
		Kind: codegen.KindAggregate,
		Expr: relalg.Aggregate{
			Input: relalg.Scan{Relation: relalg.RelationRef{Name: "events"}},
		},
	}
	if d, _ := guard.Check(context.Background(), unfiltered); d.Allow {
		t.Fatal("unfiltered aggregate should be rejected")
	}
	filtered := unfiltered
	filtered.Expr = relalg.Aggregate{Input: relalg.Select{
		Input:     relalg.Scan{Relation: relalg.RelationRef{Name: "events"}},
		Predicate: relalg.Condition{Field: "tenant_id", Op: relalg.OpEq, Value: 1},
	}}
	if d, _ := guard.Check(context.Background(), filtered); !d.Allow {
		t.Fatalf("filtered aggregate should pass: %+v", d)
	}
}

// TestAggregateGuardHintNamesExecutableRepair locks the v0.1.8 contract
// tightening: the rejection hint must contain a predicate the agent can send
// verbatim, using the primary key when known (2026-07-12 pilot attribution).
func TestAggregateGuardHintNamesExecutableRepair(t *testing.T) {
	t.Parallel()
	guard := AggregateGuard{RequirePredicate: true}
	unfiltered := codegen.Compiled{
		Kind:       codegen.KindAggregate,
		PrimaryKey: []string{"id"},
		Expr: relalg.Aggregate{
			Input: relalg.Scan{Relation: relalg.RelationRef{Name: "events"}},
		},
	}
	d, _ := guard.Check(context.Background(), unfiltered)
	if d.Allow || len(d.Hints) != 1 {
		t.Fatalf("expected one rejection hint, got %+v", d)
	}
	want := `{"field":"id","op":"is_not_null"}`
	if !strings.Contains(d.Hints[0], want) {
		t.Fatalf("hint %q must contain the executable repair %q", d.Hints[0], want)
	}
	unfiltered.PrimaryKey = nil
	d, _ = guard.Check(context.Background(), unfiltered)
	if d.Allow || len(d.Hints) != 1 || !strings.Contains(d.Hints[0], "is_not_null") {
		t.Fatalf("fallback hint must still name an executable repair, got %+v", d)
	}
}

func TestAllowedCallStillRunsCallGuard(t *testing.T) {
	t.Parallel()
	call := codegen.Compiled{Kind: codegen.KindCall, SQL: "CALL p()"}
	g := NewGate(
		StaticRule{LegacyExactSQL: true, AllowTemplates: []string{"CALL p()"}},
		CallGuard{},
	)
	if d, _ := g.Check(context.Background(), call); d.Allow {
		t.Fatal("StaticRule allow bypass must not skip CallGuard")
	}
}

func TestNewGateFromCapabilitiesWriteGuardMySQL(t *testing.T) {
	t.Parallel()
	// MySQL has no trustworthy Estimate; WriteGuard must still block non-PK
	// writes deterministically.
	caps := testdialect.MySQL{}.Capabilities()
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

type stubLayer struct {
	name     string
	phase    Phase
	decision Decision
}

func (s stubLayer) Name() string { return s.name }

func (s stubLayer) Phase() Phase { return s.phase }

func (s stubLayer) Check(context.Context, codegen.Compiled) (Decision, error) {
	return s.decision, nil
}

func TestChainGateReturnsSoftDenyFromChain(t *testing.T) {
	t.Parallel()
	g := NewGate(Estimate{
		Explainer: FakeExplainer{Plan: Plan{ScanType: ScanSeq, StatsFresh: true}},
		Threshold: Threshold{SoftScore: 70, HardScore: 30},
	})
	d, err := g.Check(context.Background(), codegen.Compiled{Kind: codegen.KindRead})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Soft || d.Allow {
		t.Fatalf("expected soft deny from chain, got %+v", d)
	}
}

func TestChainGateHardDenyWithoutRewritten(t *testing.T) {
	t.Parallel()
	g := NewGate(stubLayer{
		name:     "hard",
		decision: Decision{Allow: false, Soft: false},
	})
	d, err := g.Check(context.Background(), codegen.Compiled{SQL: "SELECT original"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Rewritten != nil {
		t.Fatal("hard deny without rewrite must not attach Rewritten")
	}
}

func TestChainGateSoftDenyAttachesLayerRewritten(t *testing.T) {
	t.Parallel()
	hint := codegen.Compiled{SQL: "SELECT hinted"}
	g := NewGate(stubLayer{
		name: "soft",
		decision: Decision{
			Allow: false, Soft: true, Rewritten: &hint,
		},
	})
	d, err := g.Check(context.Background(), codegen.Compiled{SQL: "SELECT original"})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Soft {
		t.Fatalf("expected soft deny, got %+v", d)
	}
	if d.Rewritten == nil || d.Rewritten.SQL != hint.SQL {
		t.Fatalf("soft deny with layer Rewritten must attach rewritten query, got %+v", d.Rewritten)
	}
}

func TestChainGateSoftDenyUsesFinalCompiledAfterRewrite(t *testing.T) {
	t.Parallel()
	first := codegen.Compiled{SQL: "SELECT first"}
	g := NewGate(
		stubLayer{
			name:     "rewrite",
			decision: Decision{Allow: true, Rewritten: &first},
		},
		stubLayer{
			name:     "soft",
			decision: Decision{Allow: false, Soft: true},
		},
	)
	d, err := g.Check(context.Background(), codegen.Compiled{SQL: "SELECT original", Kind: codegen.KindRead})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Soft {
		t.Fatalf("expected soft deny, got %+v", d)
	}
	if d.Rewritten == nil || d.Rewritten.SQL != first.SQL {
		t.Fatalf("expected final rewritten SQL %q, got %+v", first.SQL, d.Rewritten)
	}
}
