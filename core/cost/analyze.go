package cost

import (
	"context"
	"math/rand"

	"github.com/nethinwei/sql-mcp-server/core/codegen"
)

// AnalyzePolicy safely gates optional EXPLAIN ANALYZE sampling. Providers are
// not required to implement AnalyzeSampler.
type AnalyzePolicy struct {
	Config  AnalyzeConfig
	Sampler AnalyzeSampler
	Random  func() float64
}

// Sample runs only for compiled read-only statements when explicitly enabled.
// The bool reports whether sampling was attempted.
func (p AnalyzePolicy) Sample(ctx context.Context, c codegen.Compiled) (Plan, bool, error) {
	if !p.Config.Enabled || !p.Config.ReadOnly || !c.ReadOnly || p.Sampler == nil ||
		p.Config.SampleRate <= 0 {
		return Plan{}, false, nil
	}
	random := p.Random
	if random == nil {
		random = rand.Float64
	}
	if p.Config.SampleRate < 1 && random() >= p.Config.SampleRate {
		return Plan{}, false, nil
	}
	if p.Config.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.Config.Timeout)
		defer cancel()
	}
	plan, err := p.Sampler.ExplainAnalyze(ctx, c)
	return plan, true, err
}

// FakeAnalyzeSampler is a test implementation.
type FakeAnalyzeSampler struct {
	Plan Plan
	Err  error
}

// ExplainAnalyze implements AnalyzeSampler.
func (f FakeAnalyzeSampler) ExplainAnalyze(context.Context, codegen.Compiled) (Plan, error) {
	return f.Plan, f.Err
}
