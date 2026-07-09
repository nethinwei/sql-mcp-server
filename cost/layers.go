package cost

import (
	"context"
	"strconv"

	"github.com/nethinwei/sql-mcp-server/codegen"
)

// StaticRule applies whitelist/blacklist checks without EXPLAIN. A primary-key
// point lookup (when WhitelistPKPoint is set) bypasses the rest of the chain.
// AllowTemplates bypass EXPLAIN for known-good queries; RejectTemplates hard
// reject known-bad queries (plan baselines).
type StaticRule struct {
	PKWhitelist     bool
	AllowTemplates  []string
	RejectTemplates []string
}

// Name implements Layer.
func (s StaticRule) Name() string { return "static-rule" }

// Check implements Layer.
func (s StaticRule) Check(_ context.Context, c codegen.Compiled) (Decision, error) {
	if s.PKWhitelist && c.IsPKPoint {
		p := Plan{ScanType: ScanPoint, StatsFresh: true}
		return Decision{Allow: true, Bypass: true, Plan: &p, Score: ptrScore(ScorePlan(p))}, nil
	}
	for _, t := range s.RejectTemplates {
		if c.SQL == t {
			return Decision{Allow: false, Hints: []string{"query matches a rejected template"}}, nil
		}
	}
	for _, t := range s.AllowTemplates {
		if c.SQL == t {
			return Decision{Allow: true, Bypass: true}, nil
		}
	}
	return Decision{Allow: true}, nil
}

// Estimate is the optional EXPLAIN pre-filter layer. It runs only when the
// dialect's estimates are trustworthy (assembled by NewGateFromCapabilities).
type Estimate struct {
	Explainer Explainer
	Threshold Threshold
	Feedback  FeedbackStore
}

// Name implements Layer.
func (e Estimate) Name() string { return "estimate" }

// Check implements Layer.
func (e Estimate) Check(ctx context.Context, c codegen.Compiled) (Decision, error) {
	plan, err := e.Explainer.Explain(ctx, c.SQL, c.Args)
	if err != nil {
		// Murphy: EXPLAIN failure degrades, never panics.
		plan = Plan{ScanType: ScanUnknown, StatsFresh: false}
	}
	// Calibrate against observed history: trust reality over estimates.
	if e.Feedback != nil {
		if avg, ok := e.Feedback.AverageRows(c.SQL); ok && avg > plan.EstimatedRows {
			plan.EstimatedRows = avg
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

// Check implements Layer.
func (e EnforceCap) Check(_ context.Context, c codegen.Compiled) (Decision, error) {
	if !c.ReadOnly || e.HardRows <= 0 {
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

// Check implements Layer.
func (WriteGuard) Check(_ context.Context, c codegen.Compiled) (Decision, error) {
	if c.ReadOnly || c.IsPKPoint {
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
