package bootstrap

import (
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

// noExplainProvider is a provider without EXPLAIN.
type noExplainProvider struct{ *fakeProvider }

func (noExplainProvider) Explainer() cost.Explainer { return nil }

// A required estimate on a provider without EXPLAIN is refused at assembly,
// not run unestimated; skipping the estimate needs no EXPLAIN.
func TestEscapeExplainerRequiresEXPLAINForARequiredEstimate(t *testing.T) {
	t.Parallel()
	cfg := escapeHatchConfig(t, true)
	none := noExplainProvider{&fakeProvider{dialect: postgres.Dialect{}}}
	if _, err := escapeExplainer(cfg, "main", none, nil); err == nil ||
		!strings.Contains(err.Error(), "estimate: skip") {
		t.Fatalf("required estimate without EXPLAIN: %v", err)
	}
	if e, err := escapeExplainer(cfg, "main", &fakeProvider{dialect: postgres.Dialect{}}, nil); err != nil || e == nil {
		t.Fatalf("with EXPLAIN: %v, %v", e, err)
	}
	cfg.Cost.SQLEscape.Estimate = config.SQLEscapeEstimateSkip
	if e, err := escapeExplainer(cfg, "main", none, nil); err != nil || e != nil {
		t.Fatalf("skipped estimate: %v, %v", e, err)
	}
}
