package cost

import (
	"context"
	"strconv"

	"github.com/nethinwei/sql-mcp-server/codegen"
	"github.com/nethinwei/sql-mcp-server/relalg"
)

// StaticRule applies whitelist/blacklist checks without EXPLAIN. PK and allow
// matches bypass only optional Estimate layers; mandatory guards still run.
type StaticRule struct {
	PKWhitelist     bool
	Datasource      string
	DialectName     string
	LegacyExactSQL  bool
	AllowTemplates  []string
	RejectTemplates []string
}

// Name implements Layer.
func (s StaticRule) Name() string { return "static-rule" }

// Phase implements PhasedLayer.
func (StaticRule) Phase() Phase { return PhaseSafety }

// Check implements Layer.
func (s StaticRule) Check(_ context.Context, c codegen.Compiled) (Decision, error) {
	if matchesBaseline(s.RejectTemplates, s.Datasource, s.DialectName, c, s.LegacyExactSQL) {
		return Decision{Allow: false, Hints: []string{"query matches a rejected template"}}, nil
	}
	if s.PKWhitelist && c.IsPKPoint {
		p := Plan{ScanType: ScanPoint, StatsFresh: true}
		return Decision{Allow: true, Bypass: true, Plan: &p, Score: ptrScore(ScorePlan(p))}, nil
	}
	if matchesBaseline(s.AllowTemplates, s.Datasource, s.DialectName, c, s.LegacyExactSQL) {
		return Decision{Allow: true, Bypass: true}, nil
	}
	return Decision{Allow: true}, nil
}

// Estimate is the optional EXPLAIN pre-filter layer. It runs only when the
// dialect's estimates are trustworthy (assembled by NewGateFromCapabilities).
type Estimate struct {
	Explainer  Explainer
	Threshold  Threshold
	Feedback   FeedbackStore
	FailClosed bool
}

// EstimateOption configures NewEstimate.
type EstimateOption func(*Estimate)

// WithFailClosed rejects EXPLAIN errors and unknown plans.
func WithFailClosed() EstimateOption {
	return func(e *Estimate) { e.FailClosed = true }
}

// NewEstimate constructs an Estimate. Existing struct literals remain valid;
// the default behavior continues to degrade EXPLAIN failures.
func NewEstimate(ex Explainer, th Threshold, feedback FeedbackStore, opts ...EstimateOption) Estimate {
	e := Estimate{Explainer: ex, Threshold: th, Feedback: feedback}
	for _, opt := range opts {
		if opt != nil {
			opt(&e)
		}
	}
	return e
}

// Name implements Layer.
func (e Estimate) Name() string { return "estimate" }

// Phase implements PhasedLayer.
func (Estimate) Phase() Phase { return PhaseEstimate }

// Check implements Layer.
func (e Estimate) Check(ctx context.Context, c codegen.Compiled) (Decision, error) {
	if c.Kind != "" && c.Kind != codegen.KindRead && c.Kind != codegen.KindAggregate {
		return Decision{Allow: true}, nil
	}
	plan, err := e.Explainer.Explain(ctx, c.SQL, c.Args)
	if err != nil {
		plan = Plan{ScanType: ScanUnknown, StatsFresh: false}
	}
	// Calibrate against observed history: trust reality over estimates.
	if e.Feedback != nil {
		key := Fingerprint(e.Threshold.Datasource, e.Threshold.DialectName, c)
		if stats, ok := e.Feedback.Stats(key); ok && stats.AverageRows > plan.EstimatedRows {
			plan.EstimatedRows = stats.AverageRows
		}
	}
	score := ScorePlan(plan)
	th := e.Threshold
	deny := func(soft bool) (Decision, error) {
		return Decision{
			Allow: false, Soft: soft,
			Plan:  &plan,
			Score: ptrScore(score),
			Hints: Hints(plan, th),
		}, nil
	}
	if e.FailClosed && (err != nil || plan.ScanType == ScanUnknown) {
		return deny(false)
	}
	if plan.ScanType == ScanFull && th.RejectFullScan {
		return deny(false)
	}
	if plan.ScanType == ScanUnknown && th.RequireKnownScan {
		return deny(false)
	}
	if !plan.StatsFresh && th.RequireFreshStats {
		return deny(false)
	}
	// Note: MaxRows/MaxBytes do NOT reject here. Estimates can be wrong; the
	// EnforceCap layer uses MaxRows to wrap reads in a deterministic LIMIT as a
	// backstop, which is the whole point of defense in depth.
	//
	// Score is 0-100 where HIGHER IS SAFER (ScorePlan: point=100, full scan=20).
	// A plan scoring below HardScore is a hard reject; below SoftScore is a soft
	// reject (suggest LIMIT). Callers configure SoftScore >= HardScore.
	if th.HardScore > 0 && score.Value < th.HardScore {
		return deny(false)
	}
	if th.SoftScore > 0 && score.Value < th.SoftScore {
		return deny(true)
	}
	return Decision{Allow: true, Plan: &plan, Score: ptrScore(score)}, nil
}

// EnforceCap is the deterministic backstop: it wraps a read query in
// SELECT * FROM (<q>) sub LIMIT hard_rows so the worst case is bounded,
// independent of estimate correctness.
type EnforceCap struct {
	HardRows int64
}

// Name implements Layer.
func (e EnforceCap) Name() string { return "enforce-cap" }

// Phase implements PhasedLayer.
func (EnforceCap) Phase() Phase { return PhaseEnforcement }

// Check implements Layer.
func (e EnforceCap) Check(_ context.Context, c codegen.Compiled) (Decision, error) {
	if e.HardRows <= 0 || c.Kind == codegen.KindCall {
		return Decision{Allow: true}, nil
	}
	isRead := c.Kind == codegen.KindRead || c.Kind == codegen.KindAggregate ||
		(c.Kind == "" && c.ReadOnly)
	if !isRead {
		return Decision{Allow: true}, nil
	}
	rewritten := c
	rewritten.SQL = "SELECT * FROM (" + c.SQL + ") AS sub LIMIT " + strconv.FormatInt(e.HardRows, 10)
	return Decision{Allow: true, Rewritten: &rewritten}, nil
}

// WriteGuard is a deterministic write-safety layer, independent of EXPLAIN. A
// write (UPDATE/DELETE) whose predicate is not a full primary-key point lookup
// is hard-rejected. This backstops databases whose row estimates are
// unreliable (MySQL/OceanBase), where an unbounded WHERE could touch millions
// of rows. Point writes are already bypassed by StaticRule's PK whitelist;
// known-good bulk writes can be admitted via AllowTemplates after review.
type WriteGuard struct{}

// Name implements Layer.
func (WriteGuard) Name() string { return "write-guard" }

// Phase implements PhasedLayer.
func (WriteGuard) Phase() Phase { return PhaseEnforcement }

// Check implements Layer.
func (WriteGuard) Check(_ context.Context, c codegen.Compiled) (Decision, error) {
	if c.Kind != "" && c.Kind != codegen.KindWrite {
		return Decision{Allow: true}, nil
	}
	if c.Kind == "" && c.ReadOnly {
		return Decision{Allow: true}, nil
	}
	if c.Expr != nil {
		switch c.Expr.(type) {
		case relalg.Update, relalg.Delete:
		default:
			return Decision{Allow: true}, nil
		}
	}
	if c.IsPKPoint {
		return Decision{Allow: true}, nil
	}
	return Decision{
		Allow: false,
		Hints: []string{
			"scope the write with a primary-key equality filter (e.g. id = ...)",
			"or add the exact statement to allowTemplates after review",
		},
	}, nil
}

// CallGuard rejects CALL by default. A reviewed template must be explicitly
// listed to permit a procedure invocation.
type CallGuard struct {
	Datasource     string
	DialectName    string
	LegacyExactSQL bool
	AllowTemplates []string
}

// Name implements Layer.
func (CallGuard) Name() string { return "call-guard" }

// Phase implements PhasedLayer.
func (CallGuard) Phase() Phase { return PhaseEnforcement }

// Check implements Layer.
func (g CallGuard) Check(_ context.Context, c codegen.Compiled) (Decision, error) {
	if c.Kind != codegen.KindCall {
		return Decision{Allow: true}, nil
	}
	if matchesBaseline(g.AllowTemplates, g.Datasource, g.DialectName, c, g.LegacyExactSQL) {
		return Decision{Allow: true}, nil
	}
	return Decision{
		Allow: false,
		Hints: []string{"add the reviewed CALL fingerprint to allowTemplates"},
	}, nil
}

// AggregateGuard can require aggregate queries to carry a valid predicate.
type AggregateGuard struct {
	RequirePredicate bool
}

// Name implements Layer.
func (AggregateGuard) Name() string { return "aggregate-guard" }

// Phase implements PhasedLayer.
func (AggregateGuard) Phase() Phase { return PhaseEnforcement }

// Check implements Layer.
func (g AggregateGuard) Check(_ context.Context, c codegen.Compiled) (Decision, error) {
	if !g.RequirePredicate || c.Kind != codegen.KindAggregate {
		return Decision{Allow: true}, nil
	}
	p := compiledPredicate(c.Expr)
	if p != nil && relalg.ValidatePredicate(p) == nil {
		return Decision{Allow: true}, nil
	}
	return Decision{
		Allow: false,
		Hints: []string{"add a valid WHERE predicate to the aggregate query"},
	}, nil
}

func compiledPredicate(e relalg.Expr) relalg.Predicate {
	switch n := e.(type) {
	case relalg.Select:
		if inner := compiledPredicate(n.Input); inner != nil {
			return relalg.And{Preds: []relalg.Predicate{inner, n.Predicate}}
		}
		return n.Predicate
	case relalg.Project:
		return compiledPredicate(n.Input)
	case relalg.Sort:
		return compiledPredicate(n.Input)
	case relalg.Limit:
		return compiledPredicate(n.Input)
	case relalg.Distinct:
		return compiledPredicate(n.Input)
	case relalg.Aggregate:
		return compiledPredicate(n.Input)
	default:
		return nil
	}
}
