package tool

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

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
	rel   related
	grant grant
}

func prepareAggregate(ctx context.Context, tc Context, in aggregateInput) (aggregatePlan, error) {
	ep, pred, err := planEntity(ctx, tc, in.Entity, entity.ActionAggregate, in.Filter)
	if err != nil {
		return aggregatePlan{}, err
	}
	res, tc := ep.res, ep.tc
	aggFields, paths := splitPaths(res.Entity, aggregateFieldNames(in))
	for _, a := range in.Aggregates {
		if _, _, ok := pathOf(res.Entity, a.Field); ok {
			return aggregatePlan{}, fmt.Errorf("%w: aggregates are of the entity's own fields; group by %q instead",
				ErrInvalidInput, a.Field)
		}
	}
	rel, pred, locals, err := relate(ctx, tc, res, pred, paths)
	if err != nil {
		return aggregatePlan{}, err
	}
	aggFields = append(aggFields, locals...)
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
	return aggregatePlan{entityPlan: ep, full: andPreds(pred, g.RowFilter), rel: rel, grant: g}, nil
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
	expr := plan.rel.wrap(relalg.Scan{Relation: res.Entity.Ref()})
	if plan.full != nil {
		expr = relalg.Select{Input: expr, Predicate: plan.full}
	}
	// The result's columns are named as the request names them; the IR
	// groups by the columns they are (an alias's, a related field's).
	calls, columns, err := aggregateCalls(in.Aggregates, in.GroupBy)
	if err != nil {
		return statement{}, err
	}
	toIR := related{rename: map[string]string{}}
	groupBy := make([]string, len(in.GroupBy))
	for i, g := range in.GroupBy {
		groupBy[i] = plan.rel.field(columnOf(res.Entity, g))
		toIR.rename[g] = groupBy[i]
	}
	for i := range calls {
		calls[i].Field = columnOf(res.Entity, calls[i].Field)
	}
	expr, err = aggregateShape(res, in, relalg.Aggregate{Input: expr, GroupBy: groupBy, Aggregates: calls},
		columns, toIR, tc.MaxFilterConditions)
	if err != nil {
		return statement{}, err
	}
	compiled, err := compile(tc, res.Entity, expr)
	if err != nil {
		return statement{}, err
	}
	sql, err := gateSQL(ctx, tc, compiled)
	if err != nil {
		err = withIndexHints(ctx, tc, res.Entity, err)
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
		if a.Distinct && (a.Func != string(relalg.AggCount) || a.Field == "") {
			return nil, nil, fmt.Errorf("%w: distinct counts a field: {func: count, field, distinct: true}",
				ErrInvalidInput)
		}
		name := aggregateColumn(a)
		if slices.Contains(groupBy, name) {
			return nil, nil, fmt.Errorf("%w: aggregate %q has the name of a group-by field", ErrInvalidInput, name)
		}
		if slices.Contains(columns[len(groupBy):], name) {
			return nil, nil, fmt.Errorf("%w: aggregate %q is given twice", ErrInvalidInput, name)
		}
		calls = append(calls, relalg.AggCall{Func: f, Field: a.Field, Distinct: a.Distinct, Name: name})
		columns = append(columns, name)
	}
	return calls, columns, nil
}

// aggregateColumn is the result column of aggregate a: "count" for
// count(*), "count_distinct_<field>" for a distinct count,
// "<fn>_<field>" otherwise.
func aggregateColumn(a aggJSON) string {
	switch {
	case a.Field == "":
		return a.Func
	case a.Distinct:
		return a.Func + "_distinct_" + a.Field
	}
	return a.Func + "_" + a.Field
}

// aggregateShape adds to agg what in asks of its groups: the having filter,
// the order and the limit. Both name the result's columns (columns): the
// group-by fields and the aggregates' names.
func aggregateShape(res entity.Resolved, in aggregateInput, agg relalg.Aggregate, columns []string, toIR related,
	maxConds int,
) (relalg.Expr, error) {
	var expr relalg.Expr = agg
	having, err := filterToPredicate(in.Having, maxConds)
	if err != nil {
		return nil, err
	}
	if relalg.HasExists(having) {
		return nil, fmt.Errorf("%w: having filters the groups; exists goes in filter", ErrInvalidInput)
	}
	for _, f := range conditionFields(having) {
		if !slices.Contains(columns, f) {
			return nil, fmt.Errorf("%w: having names %q, not a group-by field or an aggregate (%s)",
				ErrInvalidInput, f, strings.Join(columns, ", "))
		}
	}
	if having != nil {
		expr = relalg.Having{Input: agg, Predicate: toIR.renamed(having)}
	}
	if len(in.OrderBy) > maxOrderTerms {
		return nil, fmt.Errorf("%w: orderBy names at most %d fields", ErrInvalidInput, maxOrderTerms)
	}
	var terms []relalg.OrderTerm
	for _, t := range in.OrderBy {
		dir := strings.ToLower(cmp.Or(t.Dir, "asc"))
		if dir != "asc" && dir != "desc" || !slices.Contains(columns, t.Field) {
			return nil, fmt.Errorf("%w: orderBy {%s %s}: a group-by field or an aggregate (%s), asc or desc",
				ErrInvalidInput, t.Field, t.Dir, strings.Join(columns, ", "))
		}
		field := toIR.field(t.Field)
		terms = append(terms, relalg.OrderTerm{Field: field, Dir: dir, Nullable: resultNullable(res, agg, field)})
	}
	if len(terms) > 0 {
		expr = relalg.Sort{Input: expr, OrderBy: terms}
	}
	if in.Limit < 0 {
		return nil, fmt.Errorf("%w: limit cannot be negative", ErrInvalidInput)
	}
	if in.Limit > 0 {
		expr = relalg.Limit{Input: expr, Count: in.Limit}
	}
	return expr, nil
}

// resultNullable reports whether the aggregate's result column name may be
// NULL: a nullable group-by field, or an aggregate other than a count (it
// is NULL over a group of NULLs).
func resultNullable(res entity.Resolved, agg relalg.Aggregate, name string) bool {
	for _, a := range agg.Aggregates {
		if a.Name == name {
			return a.Func != relalg.AggCount
		}
	}
	attr, ok := res.Entity.AttributeByName(name)
	return !ok || attr.Domain.Nullable
}
