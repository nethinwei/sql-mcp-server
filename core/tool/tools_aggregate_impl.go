package tool

import (
	"context"
	"fmt"

	"github.com/nethinwei/sql-mcp-server/core/budget"
	"github.com/nethinwei/sql-mcp-server/core/codegen"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
	"github.com/nethinwei/sql-mcp-server/core/store"
)

func runAggregate(ctx context.Context, tc Context, in aggregateInput) (Result, error) {
	ctx, cancel := withTimeout(ctx, tc)
	defer cancel()
	tc.Transaction = in.Transaction
	plan, err := prepareAggregate(ctx, tc, in)
	if err != nil {
		return Result{}, err
	}
	compiled, estimatedPlan, err := compileAggregate(ctx, plan.tc, plan.res, in, plan.full)
	if err != nil {
		return Result{}, err
	}
	out, err := scanAggregateRows(ctx, plan.tc, plan.res, compiled)
	if err != nil {
		return Result{}, err
	}
	estimatedRows := int64(0)
	if estimatedPlan != nil {
		estimatedRows = estimatedPlan.EstimatedRows
	}
	return Result{Content: out, ReturnedRows: int64(len(out)), EstimatedScannedRows: estimatedRows}, nil
}

type aggregatePlan struct {
	res  entity.Resolved
	tc   Context
	full relalg.Predicate
}

func prepareAggregate(ctx context.Context, tc Context, in aggregateInput) (aggregatePlan, error) {
	res, err := resolveDMLEntity(tc, in.Entity)
	if err != nil {
		return aggregatePlan{}, err
	}
	tc, err = routeEntity(tc, res.Entity)
	if err != nil {
		return aggregatePlan{}, err
	}
	pred, err := filterToPredicate(in.Filter)
	if err != nil {
		return aggregatePlan{}, err
	}
	aggFields := aggregateFieldNames(in)
	if err := validateFields(res, aggFields...); err != nil {
		return aggregatePlan{}, err
	}
	if err := validateUnmaskedFields(res, aggFields...); err != nil {
		return aggregatePlan{}, err
	}
	dec, err := authorize(ctx, tc, rbac.Request{
		Role: tc.Role, Subject: tc.Subject, Entity: in.Entity, Action: entity.ActionAggregate,
		ReadFields: aggFields, Predicate: pred,
	})
	if err != nil {
		return aggregatePlan{}, err
	}
	if !dec.Allowed {
		return aggregatePlan{}, denyUnauthorized(dec)
	}
	return aggregatePlan{res: res, tc: tc, full: andPreds(pred, dec.RowFilter)}, nil
}

func aggregateFieldNames(in aggregateInput) []string {
	aggFields := filterFields(in.Filter)
	aggFields = append(aggFields, in.GroupBy...)
	for _, a := range in.Aggregates {
		if a.Field != "" {
			aggFields = append(aggFields, a.Field)
		}
	}
	return aggFields
}

func compileAggregate(
	ctx context.Context,
	tc Context,
	res entity.Resolved,
	in aggregateInput,
	full relalg.Predicate,
) (codegen.Compiled, *cost.Plan, error) {
	scan := relalg.Scan{Relation: relalg.RelationRef{Name: res.Entity.Source, Schema: res.Entity.Schema}}
	var expr relalg.Expr = scan
	if full != nil {
		expr = relalg.Select{Input: scan, Predicate: full}
	}
	calls, err := aggregateCalls(in.Aggregates)
	if err != nil {
		return codegen.Compiled{}, nil, err
	}
	expr = relalg.Aggregate{Input: expr, GroupBy: in.GroupBy, Aggregates: calls}
	compiled, err := codegen.Renderer{Dialect: tc.Dialect}.Compile(expr,
		codegen.WithPrimaryKey(res.Entity.PrimaryKey()...),
		codegen.WithMaxINCardinality(effectiveMaxIN(tc)))
	if err != nil {
		return codegen.Compiled{}, nil, err
	}
	return checkGateDetailed(ctx, tc, compiled)
}

func aggregateCalls(aggregates []aggJSON) ([]relalg.AggCall, error) {
	calls := make([]relalg.AggCall, 0, len(aggregates))
	for _, a := range aggregates {
		f := relalg.AggFunc(a.Func)
		if !f.Valid() {
			return nil, fmt.Errorf("%w: aggregate func %q", ErrInvalidInput, a.Func)
		}
		calls = append(calls, relalg.AggCall{Func: f, Field: a.Field})
	}
	return calls, nil
}

func scanAggregateRows(
	ctx context.Context,
	tc Context,
	res entity.Resolved,
	compiled codegen.Compiled,
) ([]map[string]any, error) {
	rows, err := tc.DB.QueryContext(ctx, compiled.SQL, compiled.Args...)
	if err != nil {
		return nil, WrapDBError(err)
	}
	out := make([]map[string]any, 0)
	for row, err := range store.Iter(rows) {
		if err != nil {
			return nil, WrapDBError(err)
		}
		if tc.BudgetLimits.MaxReturnedRows > 0 && int64(len(out)+1) > tc.BudgetLimits.MaxReturnedRows {
			return nil, budget.ErrExceeded
		}
		if tc.Masker != nil {
			maskRow(tc.Masker, res.Attributes, row)
		}
		out = append(out, row)
	}
	return out, nil
}
