package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nethinwei/sql-mcp-server/core/config"
)

// ---- aggregate_records ----

// AggregateTool runs an aggregate query (cost-gated).
type AggregateTool struct{}

func (AggregateTool) Info() Info {
	return Info{
		Name:        "aggregate_records",
		Description: "Aggregate records of an entity",
		InputSchema: schemaAggregate,
		ReadOnly:    true,
	}
}
func (AggregateTool) Enabled(f config.ToolFlags) bool { return f.AggregateRecords }
func (AggregateTool) CostGated()                      {}
func (AggregateTool) Run(ctx context.Context, input json.RawMessage, tc Context) (Result, error) {
	var in aggregateInput
	if err := decodeInput(input, &in); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if tc.MaxFilterConditions > 0 && len(in.Filter) > tc.MaxFilterConditions {
		return Result{}, fmt.Errorf("%w: too many filter conditions", ErrInvalidInput)
	}
	if tc.MaxGroupByFields > 0 && len(in.GroupBy) > tc.MaxGroupByFields {
		return Result{}, fmt.Errorf("%w: too many group-by fields", ErrInvalidInput)
	}
	if tc.MaxAggregates > 0 && len(in.Aggregates) > tc.MaxAggregates {
		return Result{}, fmt.Errorf("%w: too many aggregates", ErrInvalidInput)
	}
	return runAggregate(ctx, tc, in)
}
