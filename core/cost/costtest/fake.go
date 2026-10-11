// Package costtest holds hand-written fakes of the cost interfaces for
// tests: an Explainer and an EXPLAIN ANALYZE sampler answering as configured.
package costtest

import (
	"context"

	"github.com/nethinwei/sql-mcp-server/core/codegen"
	"github.com/nethinwei/sql-mcp-server/core/cost"
)

// FakeExplainer is a hand-written cost.Explainer for tests.
type FakeExplainer struct {
	Plan cost.Plan
	Err  error
}

// Explain returns the configured Plan/Err.
func (f FakeExplainer) Explain(_ context.Context, _ string, _ []any) (cost.Plan, error) {
	return f.Plan, f.Err
}

// FakeAnalyzeSampler is a hand-written cost.AnalyzeSampler for tests.
type FakeAnalyzeSampler struct {
	Plan cost.Plan
	Err  error
}

// ExplainAnalyze returns the configured Plan/Err.
func (f FakeAnalyzeSampler) ExplainAnalyze(context.Context, codegen.Compiled) (cost.Plan, error) {
	return f.Plan, f.Err
}
