package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/audit"
	"github.com/nethinwei/sql-mcp-server/core/codegen"
	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/cost"
)

// ---- read_records ----

// ReadTool reads records (cost-gated).
type ReadTool struct{}

func (ReadTool) Info() Info {
	return Info{
		Name:        "read_records",
		Description: "Read records from an entity",
		InputSchema: schemaRead,
		ReadOnly:    true,
	}
}
func (ReadTool) Enabled(f config.ToolFlags) bool { return f.ReadRecords }
func (ReadTool) CostGated()                      {}

func (ReadTool) Run(ctx context.Context, input json.RawMessage, tc Context) (Result, error) {
	var in readInput
	if err := decodeInput(input, &in); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if tc.MaxFilterConditions > 0 && len(in.Filter) > tc.MaxFilterConditions {
		return Result{}, fmt.Errorf("%w: too many filter conditions", ErrInvalidInput)
	}
	if tc.MaxExpand > 0 && len(in.Expand) > tc.MaxExpand {
		return Result{}, fmt.Errorf("%w: too many relationships", ErrInvalidInput)
	}
	if err := normalizeMapValues(in.Cursor); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	return runRead(ctx, tc, in)
}

func recordReadFeedback(
	ctx context.Context,
	tc Context,
	entityName string,
	compiled codegen.Compiled,
	template string,
	estimatedPlan *cost.Plan,
	fallbackRows int64,
	fallbackDuration time.Duration,
) {
	if tc.Feedback == nil {
		return
	}
	estimatedRows := int64(0)
	if estimatedPlan != nil {
		estimatedRows = estimatedPlan.EstimatedRows
	}
	actualRows, duration := fallbackRows, fallbackDuration
	if tc.Transaction == "" {
		if plan, sampled, err := tc.Analyze.Sample(ctx, compiled); err != nil {
			samplingErr := fmt.Errorf("EXPLAIN ANALYZE sampling failed: %w", err)
			tc.Hooks.FireOnError(ctx, samplingErr)
			if tc.Auditor != nil {
				_ = tc.Auditor.Record(ctx, audit.Event{
					Time: time.Now(), Entity: entityName, Action: "explain_analyze_sample",
					Tool: "read_records", Allowed: false, Error: samplingErr.Error(),
				})
			}
		} else if sampled {
			actualRows, duration = plan.ActualRows, plan.ExecutionTime
		}
	}
	tc.Feedback.Record(cost.Feedback{
		Template: template, EstimatedRows: estimatedRows,
		ActualRows: actualRows, Duration: duration,
	})
}

func countReadRows(rows []map[string]any, expand []string) int64 {
	count := int64(len(rows))
	for _, row := range rows {
		for _, name := range expand {
			switch children := row[name].(type) {
			case []map[string]any:
				count += int64(len(children))
			case map[string]any:
				count++
			}
		}
	}
	return count
}
