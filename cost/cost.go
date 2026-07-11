package cost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nethinwei/sql-mcp-server/codegen"
	"github.com/nethinwei/sql-mcp-server/dialect"
)

// ErrCostExceeded is the sentinel for any cost-gate rejection (soft or hard).
var ErrCostExceeded = errors.New("query cost exceeded threshold")

// ScanType classifies the access path of a plan.
type ScanType uint8

const (
	// ScanUnknown means the access path could not be determined.
	ScanUnknown ScanType = iota
	// ScanSeq is a sequential scan.
	ScanSeq
	// ScanIndex is an index scan.
	ScanIndex
	// ScanPoint is a primary-key / unique-index point lookup.
	ScanPoint
	// ScanFull is a full table scan.
	ScanFull
)

// Plan is the normalized physical plan from an EXPLAIN. TotalCost is the
// dialect-native cost (reference only); decisions rely on the normalized Score.
type Plan struct {
	TotalCost       float64
	EstimatedRows   int64
	EstimatedBytes  int64
	ActualRows      int64
	ExecutionTime   time.Duration
	ScanType        ScanType
	PartitionPruned bool
	HasSort         bool
	HasTempTable    bool
	StatsFresh      bool
	Raw             json.RawMessage
}

// Score is the 0-100 normalized plan quality (higher is safer), with the risk
// factors that were hit.
type Score struct {
	Value   int
	Factors []string
}

// ScorePlan derives a normalized Score from a Plan. It is a pure function and
// the heart of cross-dialect comparability (PG cost vs MySQL cost vs SQLite
// no-cost all map to the same scale).
func ScorePlan(p Plan) Score {
	v := 100
	var factors []string
	switch p.ScanType {
	case ScanPoint:
		v = 100
	case ScanIndex:
		v = 85
	case ScanSeq:
		v = 60
	case ScanFull:
		v = 20
		factors = append(factors, "full table scan")
	case ScanUnknown:
		v = 50
		factors = append(factors, "unknown scan type")
	}
	if p.HasSort {
		v -= 5
		factors = append(factors, "sort required")
	}
	if p.HasTempTable {
		v -= 10
		factors = append(factors, "temporary table")
	}
	if !p.StatsFresh {
		v -= 15
		factors = append(factors, "stale statistics")
	}
	if p.EstimatedRows > 10000 {
		v -= 15
		factors = append(factors, "high row estimate")
	}
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	return Score{Value: v, Factors: factors}
}

// Explainer estimates a plan for one query. Implementations live in x/providers
// and parse dialect-specific EXPLAIN output.
type Explainer interface {
	Explain(ctx context.Context, query string, args []any) (Plan, error)
}

// AnalyzeSampler is an optional, read-only EXPLAIN ANALYZE capability. It is
// never invoked unless explicitly enabled by configuration.
type AnalyzeSampler interface {
	ExplainAnalyze(ctx context.Context, compiled codegen.Compiled) (Plan, error)
}

// PlanInvalidator invalidates cached plans after material feedback changes.
type PlanInvalidator interface {
	InvalidatePlan(fingerprint string)
}

// AnalyzeConfig controls optional runtime sampling. The zero value disables it.
type AnalyzeConfig struct {
	Enabled    bool
	SampleRate float64
	ReadOnly   bool
	Timeout    time.Duration
}

// Threshold configures the gate. SoftScore/HardScore are 0-100 cutoffs.
type Threshold struct {
	Enabled                   bool
	Datasource                string
	DialectName               string
	SoftScore                 int
	HardScore                 int
	MaxRows                   int64
	MaxBytes                  int64
	RejectFullScan            bool
	WhitelistPKPoint          bool
	RequirePKForWrite         bool // reject UPDATE/DELETE that is not a PK point write
	RequireAggregatePredicate bool
	ExplainFailClosed         bool
	DisableEstimate           bool
	RequireKnownScan          bool
	RequireFreshStats         bool
	LegacyExactSQL            bool
	AllowTemplates            []string
	RejectTemplates           []string
}

// Decision is a layer's verdict. Bypass skips optional estimate layers only;
// mandatory safety and enforcement layers continue to run.
type Decision struct {
	Allow     bool
	Soft      bool
	Bypass    bool
	Plan      *Plan
	Score     *Score
	Hints     []string
	Rewritten *codegen.Compiled
}

// Layer is one stage of the gate.
type Layer interface {
	Name() string
	Check(ctx context.Context, c codegen.Compiled) (Decision, error)
}

// Phase separates mandatory safety/enforcement from optional estimates.
type Phase uint8

const (
	PhaseSafety Phase = iota
	PhaseEstimate
	PhaseEnforcement
)

// PhasedLayer identifies where a layer belongs. Layers without Phase are
// treated as mandatory enforcement for backwards-compatible composition.
type PhasedLayer interface {
	Layer
	Phase() Phase
}

// Gate is the synchronous gate consulted before execution (invariant I4).
type Gate interface {
	Check(ctx context.Context, c codegen.Compiled) (Decision, error)
}

// ChainGate runs layers in order. Bypass only suppresses estimate layers;
// mandatory rejection always takes precedence over an earlier soft decision.
type ChainGate struct {
	layers []Layer
}

// NewGate chains layers into a Gate.
func NewGate(layers ...Layer) *ChainGate {
	return &ChainGate{layers: layers}
}

// Layers returns the chain's layers (for introspection/testing).
func (g *ChainGate) Layers() []Layer { return g.layers }

// Check runs the chain. A Rewritten decision updates the compiled query for
// later layers and the final return.
func (g *ChainGate) Check(ctx context.Context, c codegen.Compiled) (Decision, error) {
	rewritten := false
	bypassEstimate := false
	var result Decision
	result.Allow = true
	var soft *Decision
	for _, l := range g.layers {
		if bypassEstimate && layerPhase(l) == PhaseEstimate {
			continue
		}
		d, err := l.Check(ctx, c)
		if err != nil {
			return Decision{}, err
		}
		if d.Rewritten != nil {
			c = *d.Rewritten
			rewritten = true
		}
		if d.Bypass {
			bypassEstimate = true
			result.Bypass = true
		}
		if d.Plan != nil {
			result.Plan = d.Plan
		}
		if d.Score != nil {
			result.Score = d.Score
		}
		if len(d.Hints) > 0 {
			result.Hints = d.Hints
		}
		if !d.Allow {
			if d.Soft {
				copy := d
				soft = &copy
				continue
			}
			if rewritten {
				d.Rewritten = &c
			} else {
				d.Rewritten = nil
			}
			return d, nil
		}
	}
	if soft != nil {
		d := *soft
		if rewritten {
			d.Rewritten = &c
		}
		return d, nil
	}
	d := result
	if rewritten {
		d.Rewritten = &c
	}
	return d, nil
}

func layerPhase(l Layer) Phase {
	if phased, ok := l.(interface{ Phase() Phase }); ok {
		return phased.Phase()
	}
	return PhaseEnforcement
}

// NewGateFromCapabilities assembles the synchronous gate chain: StaticRule
// always; Estimate only when the dialect's estimates are trustworthy; EnforceCap
// when a row cap is configured. RuntimeGuard/DBNative run at execution time.
func NewGateFromCapabilities(caps dialect.Capabilities, ex Explainer, th Threshold, feedback FeedbackStore) *ChainGate {
	layers := []Layer{StaticRule{
		PKWhitelist: th.WhitelistPKPoint, Datasource: th.Datasource,
		DialectName: th.DialectName, LegacyExactSQL: th.LegacyExactSQL, AllowTemplates: th.AllowTemplates,
		RejectTemplates: th.RejectTemplates,
	}}
	if th.RequirePKForWrite {
		layers = append(layers, WriteGuard{})
	}
	layers = append(layers, CallGuard{
		Datasource: th.Datasource, DialectName: th.DialectName,
		LegacyExactSQL: th.LegacyExactSQL, AllowTemplates: th.AllowTemplates,
	})
	if th.RequireAggregatePredicate {
		layers = append(layers, AggregateGuard{RequirePredicate: true})
	}
	if !th.DisableEstimate && caps.ExplainCost && ex != nil && (caps.ExplainAccurate || th.ExplainFailClosed) {
		layers = append(layers, Estimate{
			Explainer: ex, Threshold: th, Feedback: feedback,
			FailClosed: th.ExplainFailClosed,
		})
	}
	if th.MaxRows > 0 {
		layers = append(layers, EnforceCap{HardRows: th.MaxRows})
	}
	return NewGate(layers...)
}

// ExceededError carries the estimate, score, threshold, and rewrite hints so
// the agent can rewrite and retry. Soft marks a soft reject (suggest LIMIT).
type ExceededError struct {
	Plan      Plan
	Score     Score
	Threshold Threshold
	Hints     []string
	Soft      bool
}

// Error implements error.
func (e *ExceededError) Error() string {
	kind := "hard"
	if e.Soft {
		kind = "soft"
	}
	return fmt.Sprintf("cost gate %s reject: score=%d rows=%d cost=%.0f",
		kind, e.Score.Value, e.Plan.EstimatedRows, e.Plan.TotalCost)
}

// Unwrap returns ErrCostExceeded so errors.Is works.
func (e *ExceededError) Unwrap() error { return ErrCostExceeded }

// Hints produces table-driven rewrite suggestions for a rejected plan.
func Hints(p Plan, th Threshold) []string {
	var h []string
	if p.ScanType == ScanFull {
		h = append(h, "add a filter on an indexed column or add LIMIT")
	}
	if th.MaxRows > 0 && p.EstimatedRows > th.MaxRows {
		h = append(h, "add LIMIT or narrow the filter")
	}
	if th.MaxBytes > 0 && p.EstimatedBytes > th.MaxBytes {
		h = append(h, "reduce selected columns")
	}
	if p.HasSort {
		h = append(h, "ensure an index covers the ORDER BY")
	}
	if !p.StatsFresh {
		h = append(h, "statistics may be stale; consider ANALYZE")
	}
	h = append(h, "rewrite the query and retry")
	return h
}

// FakeExplainer is a hand-written Explainer for tests.
type FakeExplainer struct {
	Plan Plan
	Err  error
}

// Explain returns the configured Plan/Err.
func (f FakeExplainer) Explain(_ context.Context, _ string, _ []any) (Plan, error) {
	return f.Plan, f.Err
}

func ptrScore(s Score) *Score { return &s }
