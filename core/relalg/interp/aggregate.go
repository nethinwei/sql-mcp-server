package interp

import (
	"fmt"
	"math/big"

	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

// evalAggregate implements γ per the spec: count(*) counts rows,
// count(field) counts non-NULL values, sum/avg/min/max skip NULLs; a global
// aggregate over empty input yields exactly one row (count 0, others NULL);
// a grouped aggregate over empty input yields zero rows; NULL group keys
// compare equal. Output columns are groupBy columns then aggregates, both in
// declaration order; comparison is positional (column names are not part of
// the cross-provider contract).
func evalAggregate(db DB, n relalg.Aggregate) (relation, error) {
	in, err := eval(db, n.Input)
	if err != nil {
		return relation{}, err
	}
	groupIdx := make([]int, len(n.GroupBy))
	for i, g := range n.GroupBy {
		j, err := in.colIndex(g)
		if err != nil {
			return relation{}, err
		}
		groupIdx[i] = j
	}
	groups, order, err := groupRows(in, groupIdx)
	if err != nil {
		return relation{}, err
	}
	if len(n.GroupBy) == 0 && len(order) == 0 {
		order = append(order, "")
		groups[""] = nil
	}
	out := relation{Cols: aggregateCols(n)}
	for _, key := range order {
		rows := groups[key]
		outRow, err := aggregateRow(in, rows, n, groupIdx)
		if err != nil {
			return relation{}, err
		}
		out.Rows = append(out.Rows, outRow)
	}
	return out, nil
}

func aggregateCols(n relalg.Aggregate) []string {
	cols := append([]string(nil), n.GroupBy...)
	for _, a := range n.Aggregates {
		cols = append(cols, string(a.Func))
	}
	return cols
}

// groupRows buckets rows by group key, keeping first-seen group order.
func groupRows(in relation, groupIdx []int) (map[string][][]any, []string, error) {
	groups := map[string][][]any{}
	var order []string
	for _, row := range in.Rows {
		keyVals := make([]any, len(groupIdx))
		for i, j := range groupIdx {
			keyVals[i] = row[j]
		}
		key, err := rowKey(keyVals)
		if err != nil {
			return nil, nil, err
		}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], row)
	}
	return groups, order, nil
}

func aggregateRow(in relation, rows [][]any, n relalg.Aggregate, groupIdx []int) ([]any, error) {
	out := make([]any, 0, len(groupIdx)+len(n.Aggregates))
	if len(rows) > 0 {
		for _, j := range groupIdx {
			out = append(out, rows[0][j])
		}
	}
	for _, call := range n.Aggregates {
		v, err := aggregateCall(in, rows, call)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func aggregateCall(in relation, rows [][]any, call relalg.AggCall) (any, error) {
	if !call.Func.Valid() {
		return nil, fmt.Errorf("%w: %q", relalg.ErrInvalidAggFunc, call.Func)
	}
	if call.Func == relalg.AggCount && call.Field == "" {
		return int64(len(rows)), nil
	}
	values, err := aggregateInput(in, rows, call)
	if err != nil {
		return nil, err
	}
	switch call.Func {
	case relalg.AggCount:
		return int64(len(values)), nil
	case relalg.AggSum:
		return sumValues(values)
	case relalg.AggAvg:
		return avgValues(values)
	default:
		return minMaxValues(values, call.Func == relalg.AggMin)
	}
}

// aggregateInput collects the non-NULL input values, deduplicated when the
// call is DISTINCT.
func aggregateInput(in relation, rows [][]any, call relalg.AggCall) ([]any, error) {
	i, err := in.colIndex(call.Field)
	if err != nil {
		return nil, err
	}
	var values []any
	seen := map[string]bool{}
	for _, row := range rows {
		v := row[i]
		if v == nil {
			continue
		}
		if call.Distinct {
			key, err := valueKey(v)
			if err != nil {
				return nil, err
			}
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		values = append(values, v)
	}
	return values, nil
}

func sumValues(values []any) (any, error) {
	if len(values) == 0 {
		return nil, nil
	}
	total := new(big.Rat)
	for _, v := range values {
		r, ok := toRat(v)
		if !ok {
			return nil, fmt.Errorf("%w: sum over %T", ErrIncomparable, v)
		}
		total.Add(total, r)
	}
	return total, nil
}

func avgValues(values []any) (any, error) {
	if len(values) == 0 {
		return nil, nil
	}
	total, err := sumValues(values)
	if err != nil {
		return nil, err
	}
	return new(big.Rat).Quo(total.(*big.Rat), new(big.Rat).SetInt64(int64(len(values)))), nil
}

func minMaxValues(values []any, min bool) (any, error) {
	if len(values) == 0 {
		return nil, nil
	}
	best := values[0]
	for _, v := range values[1:] {
		c, err := compareValues(v, best)
		if err != nil {
			return nil, err
		}
		if (min && c < 0) || (!min && c > 0) {
			best = v
		}
	}
	return best, nil
}
