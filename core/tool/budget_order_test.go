package tool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/budget"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/cost/costtest"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/internal/testdialect"
)

// A statement the cost gate refuses is refused as too costly even when it
// also exceeds the caller's scanned-row budget: the cost refusal tells how
// to narrow it, where waiting for the budget to reset never helps. A
// statement the gate allows is still held to the budget.
func TestCostRefusalComesBeforeTheBudget(t *testing.T) {
	t.Parallel()
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	full := cost.Plan{ScanType: cost.ScanFull, TableRows: 293, TableSized: true, StatsFresh: true}
	for _, c := range []struct {
		name      string
		threshold cost.Threshold
		want      error
	}{
		{"refused", cost.Threshold{RejectFullScan: true}, cost.ErrCostExceeded},
		{"allowed", cost.Threshold{SoftScore: 0, HardScore: 0}, budget.ErrExceeded},
	} {
		gate := cost.NewGate(cost.Estimate{Explainer: costtest.FakeExplainer{Plan: full}, Threshold: c.threshold})
		tc := Context{Services: &Services{Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg)}, Role: "reader",
			Dialect: testdialect.Postgres{}, Gate: gate, BudgetLimits: budget.Limits{MaxEstimatedScannedRows: 100}}
		_, err := ReadTool{}.Run(context.Background(), json.RawMessage(`{"entity":"users"}`), tc)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
	}
}

// execute_sql judges its statements in the same order.
func TestExecuteSQLCostRefusalComesBeforeTheBudget(t *testing.T) {
	t.Parallel()
	full := cost.Plan{ScanType: cost.ScanFull, TableRows: 293, TableSized: true, StatsFresh: true}
	run := func(threshold cost.Threshold) error {
		tc := escapeContext(t, threeRows(), &durableRecorder{})
		tc.SQLEscapeEstimateRequired, tc.BudgetLimits = true, budget.Limits{MaxEstimatedScannedRows: 100}
		route := tc.Escapes["crm"]
		route.Explainer, route.Threshold = costtest.FakeExplainer{Plan: full}, threshold
		tc.Escapes["crm"] = route
		_, err := RunTool(context.Background(), ExecuteSQLTool{}, sqlInput(t,
			map[string]any{"sql": "SELECT * FROM orders", "mode": "query"}), tc)
		return err
	}
	if err := run(cost.Threshold{RejectFullScan: true}); !errors.Is(err, cost.ErrCostExceeded) {
		t.Errorf("refused by the gate: %v", err)
	}
	if err := run(cost.Threshold{}); !errors.Is(err, budget.ErrExceeded) {
		t.Errorf("allowed by the gate: %v", err)
	}
}

// A full scan of a table of unknown size is refused under a scanned-row
// limit, the gate's or the budget's, and the refusal says the count is
// unknown rather than reporting the rows the scan outputs.
func TestUnsizedFullScanIsRefusedUnderScanLimits(t *testing.T) {
	t.Parallel()
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	unsized := cost.Plan{ScanType: cost.ScanFull, EstimatedRows: 1, StatsFresh: true}
	run := func(th cost.Threshold, limits budget.Limits) error {
		gate := cost.NewGate(cost.Estimate{Explainer: costtest.FakeExplainer{Plan: unsized}, Threshold: th})
		tc := Context{Services: &Services{Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg)}, Role: "reader",
			Dialect: testdialect.Postgres{}, Gate: gate, BudgetLimits: limits, DB: threeRows()}
		_, err := ReadTool{}.Run(context.Background(), json.RawMessage(`{"entity":"users"}`), tc)
		return err
	}
	err := run(cost.Threshold{MaxScannedRows: 1000}, budget.Limits{})
	d, _ := DenialFor(err, "d")
	if !errors.Is(err, cost.ErrCostExceeded) || d.Constraints["scannedRowsUnknown"] != true ||
		d.Constraints["estimatedScannedRows"] != nil {
		t.Errorf("gate limit: err %v, denial %+v", err, d)
	}
	if err := run(cost.Threshold{}, budget.Limits{MaxEstimatedScannedRows: 1000}); !errors.Is(err, budget.ErrExceeded) {
		t.Errorf("budget limit: %v", err)
	}
	if err := run(cost.Threshold{}, budget.Limits{}); err != nil {
		t.Errorf("no limit: %v", err)
	}
}
