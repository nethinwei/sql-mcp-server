package tool

import (
	"context"
	"fmt"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

func runAggregate(ctx context.Context, tc Context, in aggregateInput) (Result, error) {
	ctx, tc, end := beginCall(ctx, tc, true, in.Transaction)
	defer end()
	plan, err := prepareAggregate(ctx, tc, in)
	if err != nil {
		return Result{}, err
	}
	st, err := compileAggregate(ctx, plan, in)
	if err != nil {
		return Result{}, err
	}
	got, err := st.query(ctx)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Content:              got.rows,
		ReturnedRows:         int64(len(got.rows)),
		EstimatedScannedRows: st.sql.estimatedRows(),
	}, nil
}

type aggregatePlan struct {
	entityPlan
	full  relalg.Predicate
	grant grant
}

func prepareAggregate(ctx context.Context, tc Context, in aggregateInput) (aggregatePlan, error) {
	ep, pred, err := planEntity(ctx, tc, in.Entity, entity.ActionAggregate, in.Filter)
	if err != nil {
		return aggregatePlan{}, err
	}
	res, tc := ep.res, ep.tc
	aggFields := aggregateFieldNames(in)
	if err := validateFields(res, aggFields...); err != nil {
		return aggregatePlan{}, err
	}
	if err := validateUnmaskedFields(res, aggFields...); err != nil {
		return aggregatePlan{}, err
	}
	g, err := authorizeRequest(ctx, tc, rbac.Request{
		Role: tc.Role, Subject: tc.Subject, Entity: in.Entity, Action: entity.ActionAggregate,
		ReadFields: aggFields, Predicate: pred,
	})
	if err != nil {
		return aggregatePlan{}, err
	}
	return aggregatePlan{entityPlan: ep, full: andPreds(pred, g.RowFilter), grant: g}, nil
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

func compileAggregate(ctx context.Context, plan aggregatePlan, in aggregateInput) (statement, error) {
	tc, res := plan.tc, plan.res
	scan := relalg.Scan{Relation: res.Entity.Ref()}
	var expr relalg.Expr = scan
	if plan.full != nil {
		expr = relalg.Select{Input: scan, Predicate: plan.full}
	}
	calls, err := aggregateCalls(in.Aggregates)
	if err != nil {
		return statement{}, err
	}
	expr = relalg.Aggregate{Input: expr, GroupBy: in.GroupBy, Aggregates: calls}
	compiled, err := compile(tc, res.Entity, expr)
	if err != nil {
		return statement{}, err
	}
	sql, err := gateSQL(ctx, tc, compiled)
	if err != nil {
		return statement{}, err
	}
	return newStatement(tc, res, plan.grant, sql), nil
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
