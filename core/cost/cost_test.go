package cost_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/codegen"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/cost/costtest"
	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/core/ratelimit"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/internal/testdialect"
)

func TestScorePlan(t *testing.T) {
	t.Parallel()
	if s := cost.ScorePlan(cost.Plan{ScanType: cost.ScanFull}); s.Value >= 50 {
		t.Fatalf("full scan score %d too high", s.Value)
	}
	if s := cost.ScorePlan(cost.Plan{ScanType: cost.ScanPoint, StatsFresh: true}); s.Value != 100 {
		t.Fatalf("point score = %d, want 100", s.Value)
	}
	if s := cost.ScorePlan(cost.Plan{ScanType: cost.ScanUnknown}); s.Value != 35 {
		t.Fatalf("unknown+stale score = %d, want 35", s.Value)
	}
}

func TestEstimateHardRejectFullScan(t *testing.T) {
	t.Parallel()
	est := cost.Estimate{
		Explainer: costtest.FakeExplainer{Plan: cost.Plan{ScanType: cost.ScanFull, StatsFresh: true}},
		Threshold: cost.Threshold{RejectFullScan: true, HardScore: 40, SoftScore: 60},
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
	est := cost.Estimate{
		Explainer: costtest.FakeExplainer{Plan: cost.Plan{ScanType: cost.ScanSeq, StatsFresh: true}}, // score 60
		Threshold: cost.Threshold{SoftScore: 70, HardScore: 30},
	}
	d, _ := est.Check(context.Background(), codegen.Compiled{})
	if !d.Soft {
		t.Fatalf("expected soft reject, got %+v", d)
	}
}

func TestEstimateExplainFailureDegrades(t *testing.T) {
	t.Parallel()
	est := cost.Estimate{
		Explainer: costtest.FakeExplainer{Err: errors.New("explain failed")},
		Threshold: cost.Threshold{RequireKnownScan: true},
	}
	d, _ := est.Check(context.Background(), codegen.Compiled{})
	if d.Allow {
		t.Fatal("unknown scan with RequireKnownScan should reject")
	}
}

func TestEstimateFailClosed(t *testing.T) {
	t.Parallel()
	for _, est := range []cost.Estimate{
		{Explainer: costtest.FakeExplainer{Err: errors.New("explain failed")}, FailClosed: true},
		{Explainer: costtest.FakeExplainer{Plan: cost.Plan{ScanType: cost.ScanUnknown}}, FailClosed: true},
	} {
		d, err := est.Check(context.Background(), codegen.Compiled{Kind: codegen.KindRead})
		if err != nil {
			t.Fatal(err)
		}
		if d.Allow || d.Soft {
			t.Fatalf("fail-closed estimate must hard reject: %+v", d)
		}
	}
	defaultEstimate := cost.Estimate{Explainer: costtest.FakeExplainer{Err: errors.New("explain failed")}}
	if d, _ := defaultEstimate.Check(context.Background(), codegen.Compiled{Kind: codegen.KindRead}); !d.Allow {
		t.Fatalf("default estimate must retain fail-open compatibility: %+v", d)
	}
}

func TestEstimatePass(t *testing.T) {
	t.Parallel()
	est := cost.Estimate{
		Explainer: costtest.FakeExplainer{Plan: cost.Plan{ScanType: cost.ScanIndex, StatsFresh: true}}, // 85
		Threshold: cost.Threshold{SoftScore: 70, HardScore: 30},
	}
	d, _ := est.Check(context.Background(), codegen.Compiled{})
	if !d.Allow {
		t.Fatalf("expected pass, got %+v", d)
	}
}

func TestEnforceCapWrapsLimit(t *testing.T) {
	t.Parallel()
	ec := cost.EnforceCap{HardRows: 100}
	d, _ := ec.Check(context.Background(), codegen.Compiled{SQL: "SELECT * FROM t", ReadOnly: true})
	if d.Rewritten == nil || !strings.Contains(d.Rewritten.SQL, "LIMIT 100") {
		t.Fatalf("expected wrapped LIMIT, got %+v", d)
	}
}

// A select codegen rendered keeps its ORDER BY directly above the cap: its
// LIMIT is tightened (or added) instead of the query being wrapped, whose
// outer LIMIT would not keep the inner order (MySQL may drop it).
func TestEnforceCapKeepsTheOrderOfRenderedSelects(t *testing.T) {
	t.Parallel()
	ec := cost.EnforceCap{HardRows: 100}
	sorted := relalg.Sort{
		Input:   relalg.Scan{Relation: relalg.RelationRef{Name: "t"}},
		OrderBy: []relalg.OrderTerm{{Field: "id", Dir: "asc"}},
	}
	cases := []struct {
		name string
		expr relalg.Expr
		want string // "" when not rewritten
	}{
		{"no limit", sorted, `SELECT * FROM "t" ORDER BY "id" ASC LIMIT 100`},
		{"limit above the cap", relalg.Limit{Input: sorted, Count: 500, Offset: 20},
			`SELECT * FROM "t" ORDER BY "id" ASC LIMIT 100 OFFSET 20`},
		{"limit within the cap", relalg.Limit{Input: sorted, Count: 50}, ""},
	}
	for _, tc := range cases {
		c, err := codegen.Renderer{Dialect: testdialect.Postgres{}}.Compile(tc.expr)
		if err != nil {
			t.Fatal(err)
		}
		d, _ := ec.Check(context.Background(), c)
		switch {
		case tc.want == "" && d.Rewritten != nil:
			t.Fatalf("%s: rewritten to %q", tc.name, d.Rewritten.SQL)
		case tc.want != "" && (d.Rewritten == nil || d.Rewritten.SQL != tc.want):
			t.Fatalf("%s: got %+v, want %q", tc.name, d.Rewritten, tc.want)
		}
	}
}

func TestEnforceCapSkipsWrites(t *testing.T) {
	t.Parallel()
	ec := cost.EnforceCap{HardRows: 100}
	d, _ := ec.Check(context.Background(), codegen.Compiled{SQL: "UPDATE t SET x=1", ReadOnly: false})
	if d.Rewritten != nil {
		t.Fatal("write should not be wrapped")
	}
}

func TestEnforceCapHandlesKindsWithoutWrappingCall(t *testing.T) {
	t.Parallel()
	ec := cost.EnforceCap{HardRows: 10}
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
	sr := cost.StaticRule{PKWhitelist: true}
	d, _ := sr.Check(context.Background(), codegen.Compiled{IsKeyPoint: true})
	if !d.Allow || !d.Bypass || d.Plan == nil {
		t.Fatalf("PK point should be whitelisted with bypass, got %+v", d)
	}
}

func TestStaticRuleRejectPrecedesPKWhitelist(t *testing.T) {
	t.Parallel()
	sr := cost.StaticRule{PKWhitelist: true, LegacyExactSQL: true, RejectTemplates: []string{"SELECT blocked"}}
	d, err := sr.Check(context.Background(), codegen.Compiled{SQL: "SELECT blocked", IsKeyPoint: true})
	if err != nil {
		t.Fatal(err)
	}
	if d.Allow || d.Bypass {
		t.Fatalf("reject baseline was bypassed by PK whitelist: %+v", d)
	}
}

func TestStaticRuleTemplates(t *testing.T) {
	t.Parallel()
	sr := cost.StaticRule{
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
	primary := cost.StaticRule{
		Datasource: "primary", AllowTemplates: []string{"primary:SELECT 1"},
	}
	d, _ := primary.Check(context.Background(), compiled)
	if !d.Bypass {
		t.Fatal("datasource-qualified exact SQL did not match its source")
	}
	replica := cost.StaticRule{
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
		{name: "fingerprints", allow: []string{"fp:v4:abc"}, reject: []string{"fp:v4:def"}},
		{name: "scoped exact SQL", allow: []string{"primary:SELECT 1"}, reject: []string{"replica:SELECT bad"}},
		{name: "bare allow", allow: []string{"SELECT 1"}, want: "allowTemplates"},
		{name: "bare reject", reject: []string{"SELECT bad"}, want: "rejectTemplates"},
		{name: "unknown datasource", reject: []string{"archive:SELECT bad"}, want: "rejectTemplates"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := cost.ValidateTemplateScopes(datasources, tc.allow, tc.reject)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("cost.ValidateTemplateScopes() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) ||
				!strings.Contains(err.Error(), "fp:v4:<sha256>") ||
				!strings.Contains(err.Error(), "primary:") {
				t.Fatalf("cost.ValidateTemplateScopes() error = %v, want migration guidance for %s", err, tc.want)
			}
		})
	}
	if err := cost.ValidateTemplateScopes([]string{"primary"}, []string{"SELECT 1"}, []string{"SELECT bad"}); err != nil {
		t.Fatalf("single datasource compatibility error = %v", err)
	}
}

// A fingerprint of an earlier version, or one not written bare, matches
// nothing: a reject would stop rejecting. It is refused with one datasource
// or several.
func TestValidateTemplateScopesRefusesFingerprintsMatchingNothing(t *testing.T) {
	t.Parallel()
	for _, ds := range [][]string{{"primary"}, {"primary", "replica"}} {
		stale := []struct{ allow, reject []string }{{allow: []string{"fp:v3:abc"}}, {reject: []string{"fp:v2:def"}}}
		for _, c := range stale {
			err := cost.ValidateTemplateScopes(ds, c.allow, c.reject)
			if err == nil || !strings.Contains(err.Error(), "earlier version") || !strings.Contains(err.Error(), "fp:v4:") {
				t.Fatalf("%v %v: error = %v, want an earlier-version fingerprint refused", ds, c, err)
			}
		}
	}
	for _, value := range []string{" fp:v2:abc", "fp:v4:abc ", "primary:fp:v2:abc", "primary: fp:v4:abc"} {
		err := cost.ValidateTemplateScopes([]string{"primary"}, nil, []string{value})
		if err == nil || !strings.Contains(err.Error(), "matches nothing") {
			t.Fatalf("%q: error = %v, want it refused", value, err)
		}
	}
	if fp := cost.Fingerprint("primary", "postgres", codegen.Compiled{SQL: "SELECT 1"}); !strings.HasPrefix(fp, "fp:v4:") {
		t.Fatalf("Fingerprint = %q, want the fp:v4: prefix", fp)
	}
}

func TestChainGateShortCircuits(t *testing.T) {
	t.Parallel()
	g := cost.NewGate(
		cost.StaticRule{PKWhitelist: false},
		cost.Estimate{
			Explainer: costtest.FakeExplainer{Plan: cost.Plan{ScanType: cost.ScanFull, StatsFresh: true}},
			Threshold: cost.Threshold{RejectFullScan: true},
		},
	)
	d, _ := g.Check(context.Background(), codegen.Compiled{IsKeyPoint: false})
	if d.Allow {
		t.Fatal("expected reject")
	}
}

func TestChainGatePKWhitelistSkipsEstimate(t *testing.T) {
	t.Parallel()
	g := cost.NewGate(
		cost.StaticRule{PKWhitelist: true},
		cost.Estimate{
			Explainer: costtest.FakeExplainer{Plan: cost.Plan{ScanType: cost.ScanFull}},
			Threshold: cost.Threshold{RejectFullScan: true},
		},
	)
	d, _ := g.Check(context.Background(), codegen.Compiled{IsKeyPoint: true})
	if !d.Allow {
		t.Fatalf("PK whitelist should bypass estimate, got %+v", d)
	}
}

func TestChainGateBypassStillRunsEnforcement(t *testing.T) {
	t.Parallel()
	g := cost.NewGate(
		cost.StaticRule{PKWhitelist: true},
		cost.Estimate{
			Explainer: costtest.FakeExplainer{Plan: cost.Plan{ScanType: cost.ScanFull}},
			Threshold: cost.Threshold{RejectFullScan: true},
		},
		cost.EnforceCap{HardRows: 25},
	)
	d, err := g.Check(context.Background(), codegen.Compiled{
		Kind: codegen.KindRead, SQL: "SELECT * FROM t", ReadOnly: true, IsKeyPoint: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Allow || d.Rewritten == nil || !strings.Contains(d.Rewritten.SQL, "LIMIT 25") {
		t.Fatalf("bypass must skip cost.Estimate but retain EnforceCap: %+v", d)
	}
}

func TestCostExceededErrorUnwrap(t *testing.T) {
	t.Parallel()
	e := &cost.ExceededError{Plan: cost.Plan{ScanType: cost.ScanFull}}
	if !errors.Is(e, cost.ErrCostExceeded) {
		t.Fatal("errors.Is(cost.ErrCostExceeded) failed")
	}
}

func TestNewGateFromCapabilities(t *testing.T) {
	t.Parallel()
	caps := testdialect.Postgres{}.Capabilities()
	g := cost.NewGateFromCapabilities(caps,
		costtest.FakeExplainer{Plan: cost.Plan{ScanType: cost.ScanIndex, StatsFresh: true}},
		cost.Threshold{SoftScore: 70, HardScore: 30, MaxRows: 1000}, nil)
	d, _ := g.Check(context.Background(), codegen.Compiled{ReadOnly: true})
	if !d.Allow {
		t.Fatalf("expected pass, got %+v", d)
	}
	if d.Rewritten == nil {
		t.Fatal("expected cost.EnforceCap to wrap with LIMIT")
	}
}

func TestNewGateFromCapabilitiesSQLiteSkipsEstimate(t *testing.T) {
	t.Parallel()
	// SQLite has no EXPLAIN cost, so cost.Estimate must not be assembled.
	caps := dialect.Profile{}
	g := cost.NewGateFromCapabilities(caps, nil, cost.Threshold{MaxRows: 100}, nil)
	names := []string{}
	for _, l := range g.Layers() {
		names = append(names, l.Name())
	}
	if strings.Contains(strings.Join(names, ","), "estimate") {
		t.Fatalf("SQLite should not assemble cost.Estimate layer: %v", names)
	}
}

// The profile assembles the Estimate layer exactly as the former bools did:
// with EXPLAIN, when the estimates are trustworthy or explain fail-closed is on.
func TestNewGateFromCapabilitiesMatchesTheFormerBools(t *testing.T) {
	t.Parallel()
	ex := costtest.FakeExplainer{Plan: cost.Plan{ScanType: cost.ScanIndex, StatsFresh: true}}
	for _, explain := range []bool{false, true} {
		for _, accurate := range []dialect.Strength{dialect.Unsupported, dialect.BestEffort, dialect.Enforced} {
			for _, failClosed := range []bool{false, true} {
				caps := dialect.Profile{dialect.ExplainAccurate: {Strength: accurate}}
				if explain {
					caps[dialect.Explain] = dialect.Claim{Strength: dialect.Enforced}
				}
				g := cost.NewGateFromCapabilities(caps, ex, cost.Threshold{ExplainFailClosed: failClosed}, nil)
				got := false
				for _, l := range g.Layers() {
					got = got || l.Name() == "estimate"
				}
				want := explain && (accurate == dialect.Enforced || failClosed)
				if got != want {
					t.Errorf("explain=%v accurate=%s failClosed=%v: estimate=%v, want %v",
						explain, accurate, failClosed, got, want)
				}
			}
		}
	}
}

func TestWriteGuard(t *testing.T) {
	t.Parallel()
	var wg cost.WriteGuard
	if d, _ := wg.Check(context.Background(), codegen.Compiled{ReadOnly: true}); !d.Allow {
		t.Fatal("read should pass the write guard")
	}
	if d, _ := wg.Check(context.Background(), codegen.Compiled{IsKeyPoint: true}); !d.Allow {
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
	if d, _ := (cost.CallGuard{}).Check(context.Background(), call); d.Allow {
		t.Fatal("CALL should be denied by default")
	}
	guard := cost.CallGuard{LegacyExactSQL: true, AllowTemplates: []string{"CALL p()"}}
	if d, _ := guard.Check(context.Background(), call); !d.Allow {
		t.Fatalf("reviewed CALL should be allowed: %+v", d)
	}
}

func TestAggregateGuardRequiresPredicate(t *testing.T) {
	t.Parallel()
	guard := cost.AggregateGuard{RequirePredicate: true}
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
	guard := cost.AggregateGuard{RequirePredicate: true}
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
	g := cost.NewGate(
		cost.StaticRule{LegacyExactSQL: true, AllowTemplates: []string{"CALL p()"}},
		cost.CallGuard{},
	)
	if d, _ := g.Check(context.Background(), call); d.Allow {
		t.Fatal("cost.StaticRule allow bypass must not skip cost.CallGuard")
	}
}

func TestNewGateFromCapabilitiesWriteGuardMySQL(t *testing.T) {
	t.Parallel()
	// MySQL has no trustworthy cost.Estimate; cost.WriteGuard must still block non-PK
	// writes deterministically.
	caps := testdialect.MySQL{}.Capabilities()
	g := cost.NewGateFromCapabilities(caps, nil, cost.Threshold{RequirePKForWrite: true, MaxRows: 1000}, nil)
	d, err := g.Check(context.Background(), codegen.Compiled{SQL: "UPDATE t SET x=1", IsKeyPoint: false})
	if err != nil {
		t.Fatal(err)
	}
	if d.Allow {
		t.Fatal("non-PK write must be rejected by cost.WriteGuard on MySQL")
	}
	dpk, err := g.Check(context.Background(), codegen.Compiled{SQL: "UPDATE t SET x=1 WHERE id=1", IsKeyPoint: true})
	if err != nil {
		t.Fatal(err)
	}
	if !dpk.Allow {
		t.Fatal("PK point write should pass")
	}
}

type stubLayer struct {
	name     string
	phase    cost.Phase
	decision cost.Decision
}

func (s stubLayer) Name() string { return s.name }

func (s stubLayer) Phase() cost.Phase { return s.phase }

func (s stubLayer) Check(context.Context, codegen.Compiled) (cost.Decision, error) {
	return s.decision, nil
}

func TestChainGateReturnsSoftDenyFromChain(t *testing.T) {
	t.Parallel()
	g := cost.NewGate(cost.Estimate{
		Explainer: costtest.FakeExplainer{Plan: cost.Plan{ScanType: cost.ScanSeq, StatsFresh: true}},
		Threshold: cost.Threshold{SoftScore: 70, HardScore: 30},
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
	g := cost.NewGate(stubLayer{
		name:     "hard",
		decision: cost.Decision{Allow: false, Soft: false},
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
	g := cost.NewGate(stubLayer{
		name: "soft",
		decision: cost.Decision{
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
	g := cost.NewGate(
		stubLayer{
			name:     "rewrite",
			decision: cost.Decision{Allow: true, Rewritten: &first},
		},
		stubLayer{
			name:     "soft",
			decision: cost.Decision{Allow: false, Soft: true},
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

// A full scan of a small table scores as an index scan; of a large one, of one
// whose size is unknown, or with the allowance off, it is rejected. The size
// is the table's: a filter that outputs one row of a large table still reads
// all of them.
func TestEstimateSmallTableFullScan(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		output, table, limit int64
		sized, allow         bool
	}{
		{output: 1, table: 1, limit: 1000, sized: true, allow: true},
		{output: 71, table: 71, limit: 1000, sized: true, allow: true},
		{output: 1, table: 1000, limit: 1000, sized: true, allow: true},
		{output: 1, table: 1001, limit: 1000, sized: true, allow: false},
		{output: 1, table: 5_000_000, limit: 1000, sized: true, allow: false},
		{output: 1, table: 0, limit: 1000, sized: false, allow: false},
		{output: 71, table: 71, limit: 0, sized: true, allow: false},
	} {
		est := cost.Estimate{
			Explainer: costtest.FakeExplainer{Plan: cost.Plan{
				ScanType: cost.ScanFull, EstimatedRows: c.output, TableRows: c.table, TableSized: c.sized, StatsFresh: true,
			}},
			Threshold: cost.Threshold{RejectFullScan: true, FullScanMaxRows: c.limit, HardScore: 40, SoftScore: 60},
		}
		d, err := est.Check(context.Background(), codegen.Compiled{SQL: "SELECT * FROM t"})
		if err != nil {
			t.Fatal(err)
		}
		if d.Allow != c.allow {
			t.Errorf("%+v: allow = %v", c, d.Allow)
		}
	}
}

// An EXPLAIN the connection refused (its breaker open) is the call's error,
// not an estimate to judge: the hold-back is reported as such, never as a
// cost denial.
func TestEstimateReportsARefusedExplain(t *testing.T) {
	// So is one naming a table or a column the database no longer has: the
	// schema changed, which no rewrite of the query can help.
	for _, refused := range []error{ratelimit.ErrCircuitOpen, store.ErrMissingColumn, store.ErrMissingRelation} {
		est := cost.Estimate{Explainer: costtest.FakeExplainer{Err: refused}, FailClosed: true}
		_, err := est.Check(context.Background(), codegen.Compiled{SQL: "SELECT * FROM t"})
		if !errors.Is(err, refused) {
			t.Fatalf("a refused EXPLAIN (%v) became %v", refused, err)
		}
	}
}

// A soft refusal reports the chain's last plan and score, and every hint,
// not the ones current when the soft layer ran.
func TestChainGateSoftDenyCarriesTheFinalDiagnosis(t *testing.T) {
	t.Parallel()
	early, late := cost.Plan{EstimatedRows: 1}, cost.Plan{EstimatedRows: 99}
	g := cost.NewGate(
		stubLayer{name: "soft", phase: cost.PhaseEstimate, decision: cost.Decision{
			Soft: true, Plan: &early, Score: &cost.Score{Value: 50}, Hints: []string{"add LIMIT", "retry"},
		}},
		stubLayer{name: "later", decision: cost.Decision{
			Allow: true, Bypass: true, Plan: &late, Score: &cost.Score{Value: 40}, Hints: []string{"retry", "narrow"},
		}},
	)
	d, err := g.Check(context.Background(), codegen.Compiled{Kind: codegen.KindRead})
	if err != nil {
		t.Fatal(err)
	}
	if d.Allow || !d.Soft || d.Plan != &late || d.Score.Value != 40 || !d.Bypass ||
		!slices.Equal(d.Hints, []string{"add LIMIT", "retry", "narrow"}) {
		t.Fatalf("soft deny = %+v", d)
	}
}
