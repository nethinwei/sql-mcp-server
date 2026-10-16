package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
)

// ---- aggregate_records ----

// AggregateTool runs an aggregate query (cost-gated).
type AggregateTool struct{}

func (AggregateTool) Info() Info {
	return Info{
		Name:   "aggregate_records",
		Action: entity.ActionAggregate.String(),
		Title:  "Aggregate records",
		Description: "Count, sum, average, min or max the rows of one entity, optionally grouped, returned " +
			"as {\"result\": [rows]}. Prefer it to reading rows when you need totals; filter narrowly, " +
			"since the same cost gate as read_records applies.",
		InputSchema: schemaAggregate,
		ReadOnly:    true, Idempotent: true,
	}
}
func (AggregateTool) Enabled(f config.ToolFlags) bool { return f.AggregateRecords }
func (AggregateTool) CostGated()                      {}
func (AggregateTool) Run(ctx context.Context, input json.RawMessage, tc Context) (Result, error) {
	in, err := decodeTool[aggregateInput](input, nil)
	if err != nil {
		return Result{}, err
	}
	if tc.MaxGroupByFields > 0 && len(in.GroupBy) > tc.MaxGroupByFields {
		return Result{}, fmt.Errorf("%w: too many group-by fields", ErrInvalidInput)
	}
	if tc.MaxAggregates > 0 && len(in.Aggregates) > tc.MaxAggregates {
		return Result{}, fmt.Errorf("%w: too many aggregates", ErrInvalidInput)
	}
	return runAggregate(ctx, tc, in)
}
