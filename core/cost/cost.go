package cost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/codegen"
	"github.com/nethinwei/sql-mcp-server/core/dialect"
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

// scanRisk orders scan types from the safest to the riskiest.
var scanRisk = map[ScanType]int{ScanPoint: 0, ScanIndex: 1, ScanSeq: 2, ScanUnknown: 3, ScanFull: 4}

// Worse reports whether a is a riskier access than b. A plan reading several
// tables (a join, or a table under a grouping or sorting step) is as risky
// as its worst access.
func Worse(a, b ScanType) bool { return scanRisk[a] > scanRisk[b] }

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
	// TableRows is how many rows the largest table a full scan reads holds:
	// what the scan reads, not what it outputs after filtering. TableSized
	// reports that a provider measured it for every full scan; without it a
	// full scan is never taken for a small table's.
	TableRows  int64
	TableSized bool
	Raw        json.RawMessage
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
	if penalty := rowPenalty(p.EstimatedRows); penalty > 0 {
		v -= penalty
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

// rowPenalty grows with the order of magnitude of the rows a plan reads: an
// index range over ten million rows is no cheaper for using an index.
func rowPenalty(rows int64) int {
	switch {
	case rows > 10_000_000:
		return 45
	case rows > 1_000_000:
		return 35
	case rows > 100_000:
		return 25
	case rows > 10_000:
		return 15
	}
	return 0
}

// ScannedRows is how many rows the plan reads: its largest row estimate, or
// the table a full scan reads whole when that is larger (a filter may output
// one row after reading all of them). See ScannedRowsKnown for a full scan of
// a table no one measured.
func (p Plan) ScannedRows() int64 {
	rows, _ := p.ScannedRowsKnown()
	return rows
}

// ScannedRowsKnown is ScannedRows, and whether it is known: a full scan of a
// table whose size the provider did not measure reads all of its rows, which
// the output estimate does not tell; its count is then the output estimate,
// a floor, and a scanned-rows limit refuses it.
func (p Plan) ScannedRowsKnown() (int64, bool) {
	if p.TableSized {
		// Its full scans read their tables whole, those of small tables
		// taken for index scans too (see smallTable).
		return max(p.EstimatedRows, p.TableRows), true
	}
	return p.EstimatedRows, p.ScanType != ScanFull
}

// OverScanLimit reports a plan reading more rows than limit allows (0 is no
// limit), or an unknown number of them under a limit.
func (p Plan) OverScanLimit(limit int64) bool {
	rows, known := p.ScannedRowsKnown()
	return limit > 0 && (!known || rows > limit)
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
	Datasource  string
	DialectName string
	SoftScore   int
	HardScore   int
	MaxRows     int64
	MaxBytes    int64
	// MaxScannedRows refuses a plan reading more rows than this (see
	// Plan.ScannedRows), whatever its access path; 0 is unlimited.
	MaxScannedRows int64
	RejectFullScan bool
	// FullScanMaxRows: a full scan of tables of no more than this many rows
	// (Plan.TableRows, when sized) is a small table's, scored and admitted as
	// a sequential scan.
	FullScanMaxRows           int64
	WhitelistPKPoint          bool
	RequirePKForWrite         bool // reject UPDATE/DELETE that is not a PK point write
	RequireAggregatePredicate bool
	ExplainFailClosed         bool
	RequireKnownScan          bool
	RequireFreshStats         bool
	LegacyExactSQL            bool
	AllowTemplates            []string
	RejectTemplates           []string
}

// Tightening is a caller's own stricter gate (a role's or a user's limits):
// a set score raises the threshold's, RejectFullScan adds to it. It never
// loosens the gate.
type Tightening struct {
	SoftScore, HardScore int
	RejectFullScan       bool
	MaxScannedRows       int64
}

// Tighten is th as strict as t asks, never less strict than th.
func (th Threshold) Tighten(t Tightening) Threshold {
	th.SoftScore = max(th.SoftScore, t.SoftScore)
	th.HardScore = max(th.HardScore, t.HardScore)
	th.RejectFullScan = th.RejectFullScan || t.RejectFullScan
	if t.MaxScannedRows > 0 && (th.MaxScannedRows <= 0 || t.MaxScannedRows < th.MaxScannedRows) {
		th.MaxScannedRows = t.MaxScannedRows
	}
	return th
}

type tighteningKey struct{}

// WithTightening carries the caller's tightening to the gates a call checks
// its statements with (see Estimate).
func WithTightening(ctx context.Context, t Tightening) context.Context {
	if t == (Tightening{}) {
		return ctx
	}
	return context.WithValue(ctx, tighteningKey{}, t)
}

func tighteningOf(ctx context.Context) Tightening {
	t, _ := ctx.Value(tighteningKey{}).(Tightening)
	return t
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
	// Threshold is the one a refusal was judged by, to report its limits.
	Threshold Threshold
}

// Estimate is the plan and score the decision carries, zero when it has none.
func (d Decision) Estimate() (Plan, Score) {
	var plan Plan
	var score Score
	if d.Plan != nil {
		plan = *d.Plan
	}
	if d.Score != nil {
		score = *d.Score
	}
	return plan, score
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
	var soft *Decision // the last soft refusal, which the chain's result reports
	for _, l := range g.layers {
		if bypassEstimate && layerPhase(l) == PhaseEstimate {
			continue
		}
		d, err := l.Check(ctx, c)
		if err != nil {
			return Decision{}, err
		}
		c, rewritten = g.applyLayerDecision(&result, c, rewritten, &bypassEstimate, &soft, d)
		if !d.Allow && !d.Soft {
			return g.hardDenyDecision(d, c, rewritten), nil
		}
	}
	if soft != nil {
		result.Allow, result.Soft, result.Threshold = false, true, soft.Threshold
	}
	return g.attachRewritten(result, c, rewritten), nil
}

func (g *ChainGate) applyLayerDecision(
	result *Decision,
	c codegen.Compiled,
	rewritten bool,
	bypassEstimate *bool,
	soft **Decision,
	d Decision,
) (codegen.Compiled, bool) {
	if d.Rewritten != nil {
		c = *d.Rewritten
		rewritten = true
	}
	if d.Bypass {
		*bypassEstimate = true
		result.Bypass = true
	}
	if d.Plan != nil {
		result.Plan = d.Plan
	}
	if d.Score != nil {
		result.Score = d.Score
	}
	for _, h := range d.Hints {
		if !slices.Contains(result.Hints, h) {
			result.Hints = append(result.Hints, h)
		}
	}
	if !d.Allow && d.Soft {
		copy := d
		*soft = &copy
	}
	return c, rewritten
}

func (g *ChainGate) hardDenyDecision(d Decision, c codegen.Compiled, rewritten bool) Decision {
	if rewritten {
		d.Rewritten = &c
	} else {
		d.Rewritten = nil
	}
	return d
}

func (g *ChainGate) attachRewritten(d Decision, c codegen.Compiled, rewritten bool) Decision {
	if rewritten {
		d.Rewritten = &c
	}
	return d
}

func layerPhase(l Layer) Phase {
	if phased, ok := l.(PhasedLayer); ok {
		return phased.Phase()
	}
	return PhaseEnforcement
}

// NewGateFromCapabilities assembles the synchronous gate chain: StaticRule
// always; Estimate only when the connection has EXPLAIN and its estimates are
// enforced as trustworthy, or explain fail-closed rejects whatever they cannot
// show; EnforceCap when a row cap is configured. RuntimeGuard/DBNative run at
// execution time.
func NewGateFromCapabilities(caps dialect.Profile, ex Explainer, th Threshold, feedback FeedbackStore) *ChainGate {
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
	if caps.Has(dialect.Explain) && ex != nil && (caps.Enforced(dialect.ExplainAccurate) || th.ExplainFailClosed) {
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
	if scanned, known := p.ScannedRowsKnown(); p.OverScanLimit(th.MaxScannedRows) {
		if known {
			h = append(h, fmt.Sprintf("estimated to read %d rows, over the limit of %d: narrow the filter "+
				"(a shorter time range, a more selective condition) or split the work into smaller ranges",
				scanned, th.MaxScannedRows))
		} else {
			h = append(h, fmt.Sprintf("reads a whole table whose row count is unknown, so it may exceed the "+
				"limit of %d rows: filter on an indexed column, or ask an administrator to run ANALYZE "+
				"on the table", th.MaxScannedRows))
		}
	}
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

func ptrScore(s Score) *Score { return &s }
