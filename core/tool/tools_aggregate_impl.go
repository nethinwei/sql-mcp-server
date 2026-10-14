package tool

import (
	"context"
	"fmt"
	"slices"

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
	calls, columns, err := aggregateCalls(in.Aggregates, in.GroupBy)
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
	st := newStatement(tc, res, plan.grant, sql)
	st.columns = columns
	return st, nil
}

// aggregateCalls compiles the aggregates and names the result columns by
// position the same on every database: the group-by fields, then "count" for
// count(*) and "<func>_<field>" for the others (databases name an aggregate
// column differently, and PostgreSQL names two sums alike). A name that is
// also a group-by field would hide one of the two columns. The names stay out
// of the SQL, so its fingerprint (allowTemplates, rejectTemplates, feedback)
// does not change. An aggregate given twice would hide one of its columns
// too.
func aggregateCalls(aggregates []aggJSON, groupBy []string) ([]relalg.AggCall, []string, error) {
	calls := make([]relalg.AggCall, 0, len(aggregates))
	columns := append(make([]string, 0, len(groupBy)+len(aggregates)), groupBy...)
	for _, a := range aggregates {
		f := relalg.AggFunc(a.Func)
		if !f.Valid() {
			return nil, nil, fmt.Errorf("%w: aggregate func %q", ErrInvalidInput, a.Func)
		}
		name := aggregateColumn(a.Func, a.Field)
		if slices.Contains(groupBy, name) {
			return nil, nil, fmt.Errorf("%w: aggregate %q has the name of a group-by field", ErrInvalidInput, name)
		}
		if slices.Contains(columns[len(groupBy):], name) {
			return nil, nil, fmt.Errorf("%w: aggregate %q is given twice", ErrInvalidInput, name)
		}
		calls = append(calls, relalg.AggCall{Func: f, Field: a.Field})
		columns = append(columns, name)
	}
	return calls, columns, nil
}

// aggregateColumn is the result column of aggregate fn over field: "count"
// for count(*), "<fn>_<field>" otherwise.
func aggregateColumn(fn, field string) string {
	if field == "" {
		return fn
	}
	return fn + "_" + field
}
