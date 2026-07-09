package cost

import (
	"context"
	"strconv"

	"github.com/nethinwei/sql-mcp-server/codegen"
)

// StaticRule applies whitelist/blacklist checks without EXPLAIN. A primary-key
// point lookup (when WhitelistPKPoint is set) bypasses the rest of the chain.
type StaticRule struct {
	PKWhitelist bool
}

// Name implements Layer.
func (s StaticRule) Name() string { return "static-rule" }

// Check implements Layer.
func (s StaticRule) Check(_ context.Context, c codegen.Compiled) (Decision, error) {
	if s.PKWhitelist && c.IsPKPoint {
		p := Plan{ScanType: ScanPoint, StatsFresh: true}
		return Decision{Allow: true, Bypass: true, Plan: &p, Score: ptrScore(ScorePlan(p))}, nil
	}
	return Decision{Allow: true}, nil
}

// Estimate is the optional EXPLAIN pre-filter layer. It runs only when the
// dialect's estimates are trustworthy (assembled by NewGateFromCapabilities).
type Estimate struct {
	Explainer Explainer
	Threshold Threshold
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
	if th.MaxRows > 0 && plan.EstimatedRows > th.MaxRows {
		return deny(false)
	}
	if th.MaxBytes > 0 && plan.EstimatedBytes > th.MaxBytes {
		return deny(false)
	}
	if th.HardScore > 0 && score.Value >= th.HardScore {
		return deny(false)
	}
	if th.SoftScore > 0 && score.Value >= th.SoftScore {
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
