package tool

import (
	"context"

	"github.com/nethinwei/sql-mcp-server/core/codegen"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

type writePlan struct {
	res entity.Resolved
	tc  Context
}

func runInsert(ctx context.Context, tc Context, in createInput) (Result, error) {
	ctx, cancel := withTimeout(ctx, tc, 0)
	defer cancel()
	tc, endMetering := meterCall(tc, false)
	defer endMetering()
	tc.Transaction = in.Transaction
	st, err := prepareInsert(ctx, tc, in)
	if err != nil {
		return Result{}, err
	}
	if st.tc.Dialect.Capabilities().Returning && len(st.res.Entity.PrimaryKey()) > 0 {
		return insertWithReturning(ctx, st)
	}
	return insertWithExec(ctx, st, in)
}

func prepareInsert(ctx context.Context, tc Context, in createInput) (statement, error) {
	res, err := resolveDMLEntity(tc, in.Entity)
	if err != nil {
		return statement{}, err
	}
	tc, err = routeEntity(ctx, tc, res.Entity, entity.ActionCreate)
	if err != nil {
		return statement{}, err
	}
	keys := sortedKeys(in.Values)
	if err := validateFields(res, keys...); err != nil {
		return statement{}, err
	}
	if err := validateWritable(res, keys...); err != nil {
		return statement{}, err
	}
	g, err := authorizeRequest(ctx, tc, rbac.Request{
		Role: tc.Role, Subject: tc.Subject, Entity: in.Entity, Action: entity.ActionCreate,
		WriteFields: keys,
	})
	if err != nil {
		return statement{}, err
	}
	tup := make(relalg.Tuple, len(keys))
	for i, k := range keys {
		tup[i] = in.Values[k]
	}
	target := relalg.RelationRef{Name: res.Entity.Source, Schema: res.Entity.Schema}
	compiled, err := codegen.Renderer{Dialect: tc.Dialect}.Compile(
		relalg.Insert{Target: target, Columns: keys, Tuples: []relalg.Tuple{tup}},
		codegen.WithPrimaryKey(res.Entity.PrimaryKey()...),
		codegen.WithMaxINCardinality(effectiveMaxIN(tc)),
	)
	if err != nil {
		return statement{}, err
	}
	st := newStatement(tc, res, g, admitInsert(compiled))
	st.write = true
	return st, nil
}

func insertWithReturning(ctx context.Context, st statement) (Result, error) {
	got, err := st.query(ctx)
	if err != nil {
		return Result{}, err
	}
	if len(got.rows) == 0 {
		return Result{Content: []map[string]any{{"rowsAffected": int64(0)}}}, nil
	}
	row := got.rows[0]
	row["rowsAffected"] = int64(1)
	return Result{Content: []map[string]any{row}}, nil
}

func insertWithExec(ctx context.Context, st statement, in createInput) (Result, error) {
	r, err := st.exec(ctx)
	if err != nil {
		return Result{}, err
	}
	row := insertedKey(st.res.Entity, in.Values, r.LastInsertID)
	st.present(row)
	row["lastInsertId"], row["rowsAffected"] = r.LastInsertID, r.RowsAffected
	return Result{Content: []map[string]any{row}}, nil
}

// insertedKey reports the identity key of a row inserted without RETURNING:
// the values the caller supplied, and the auto-increment column from the
// driver's last insert id.
func insertedKey(e entity.Entity, values map[string]any, lastInsertID int64) map[string]any {
	row := map[string]any{}
	keys := e.IdentityKeys(true, false)
	if len(keys) == 0 {
		return row
	}
	for _, column := range keys[0] {
		if v, ok := values[column]; ok {
			row[column] = v
		} else if a, ok := e.AttributeByName(column); ok && a.Domain.AutoIncrement && lastInsertID != 0 {
			row[column] = lastInsertID
		}
	}
	return row
}

// runFilteredWrite executes a filtered UPDATE or DELETE built by prepare.
func runFilteredWrite(
	ctx context.Context,
	tc Context,
	transaction string,
	prepare func(context.Context, Context) (statement, error),
) (Result, error) {
	ctx, cancel := withTimeout(ctx, tc, 0)
	defer cancel()
	tc, endMetering := meterCall(tc, false)
	defer endMetering()
	tc.Transaction = transaction
	st, err := prepare(ctx, tc)
	if err != nil {
		return Result{}, err
	}
	r, err := st.exec(ctx)
	if err != nil {
		return Result{}, err
	}
	return Result{Content: []map[string]any{{"rowsAffected": r.RowsAffected}}}, nil
}

func prepareUpdate(ctx context.Context, tc Context, in updateInput) (statement, error) {
	plan, pred, err := prepareFilteredWrite(ctx, tc, in.Entity, entity.ActionUpdate, in.Filter)
	if err != nil {
		return statement{}, err
	}
	updFields := append(filterFields(in.Filter), sortedKeys(in.Set)...)
	if err := validateFields(plan.res, updFields...); err != nil {
		return statement{}, err
	}
	if err := validateUnmaskedFields(plan.res, filterFields(in.Filter)...); err != nil {
		return statement{}, err
	}
	if err := validateWritable(plan.res, sortedKeys(in.Set)...); err != nil {
		return statement{}, err
	}
	g, err := authorizeRequest(ctx, plan.tc, rbac.Request{
		Role: plan.tc.Role, Subject: plan.tc.Subject, Entity: in.Entity, Action: entity.ActionUpdate,
		ReadFields: filterFields(in.Filter), WriteFields: sortedKeys(in.Set), Predicate: pred,
	})
	if err != nil {
		return statement{}, err
	}
	full := andPreds(pred, g.RowFilter)
	if full == nil {
		return statement{}, ErrUnsafeWrite
	}
	if err := checkCascades(ctx, plan.tc, plan.res.Entity, entity.ActionUpdate, sortedKeys(in.Set)); err != nil {
		return statement{}, err
	}
	setItems := make([]relalg.SetItem, 0, len(in.Set))
	for _, k := range sortedKeys(in.Set) {
		setItems = append(setItems, relalg.SetItem{Field: k, Value: in.Set[k]})
	}
	target := relalg.RelationRef{Name: plan.res.Entity.Source, Schema: plan.res.Entity.Schema}
	compiled, err := codegen.Renderer{Dialect: plan.tc.Dialect}.Compile(
		relalg.Update{Target: target, Predicate: full, Set: setItems},
		codegen.WithPrimaryKey(plan.res.Entity.PrimaryKey()...),
		codegen.WithIdentityKeys(plan.res.Entity.IdentityKeys(true, plan.tc.Transaction != "")...),
		codegen.WithMaxINCardinality(effectiveMaxIN(plan.tc)),
	)
	if err != nil {
		return statement{}, err
	}
	return plan.statement(ctx, g, compiled)
}

func prepareDelete(ctx context.Context, tc Context, in deleteInput) (statement, error) {
	plan, pred, err := prepareFilteredWrite(ctx, tc, in.Entity, entity.ActionDelete, in.Filter)
	if err != nil {
		return statement{}, err
	}
	if err := validateFields(plan.res, filterFields(in.Filter)...); err != nil {
		return statement{}, err
	}
	if err := validateUnmaskedFields(plan.res, filterFields(in.Filter)...); err != nil {
		return statement{}, err
	}
	g, err := authorizeRequest(ctx, plan.tc, rbac.Request{
		Role: plan.tc.Role, Subject: plan.tc.Subject, Entity: in.Entity, Action: entity.ActionDelete,
		ReadFields: filterFields(in.Filter), Predicate: pred,
	})
	if err != nil {
		return statement{}, err
	}
	full := andPreds(pred, g.RowFilter)
	if full == nil {
		return statement{}, ErrUnsafeWrite
	}
	if err := checkCascades(ctx, plan.tc, plan.res.Entity, entity.ActionDelete, nil); err != nil {
		return statement{}, err
	}
	target := relalg.RelationRef{Name: plan.res.Entity.Source, Schema: plan.res.Entity.Schema}
	compiled, err := codegen.Renderer{Dialect: plan.tc.Dialect}.Compile(
		relalg.Delete{Target: target, Predicate: full},
		codegen.WithPrimaryKey(plan.res.Entity.PrimaryKey()...),
		codegen.WithIdentityKeys(plan.res.Entity.IdentityKeys(true, plan.tc.Transaction != "")...),
		codegen.WithMaxINCardinality(effectiveMaxIN(plan.tc)),
	)
	if err != nil {
		return statement{}, err
	}
	return plan.statement(ctx, g, compiled)
}

// statement gates compiled, the write of the plan.
func (p writePlan) statement(ctx context.Context, g grant, compiled codegen.Compiled) (statement, error) {
	sql, err := gateSQL(ctx, p.tc, compiled)
	if err != nil {
		return statement{}, err
	}
	st := newStatement(p.tc, p.res, g, sql)
	st.write = true
	return st, nil
}

func prepareFilteredWrite(
	ctx context.Context,
	tc Context,
	entityName string,
	action entity.Action,
	filter []condJSON,
) (writePlan, relalg.Predicate, error) {
	res, err := resolveDMLEntity(tc, entityName)
	if err != nil {
		return writePlan{}, nil, err
	}
	tc, err = routeEntity(ctx, tc, res.Entity, action)
	if err != nil {
		return writePlan{}, nil, err
	}
	pred, err := filterToPredicate(filter, tc.MaxFilterConditions)
	if err != nil {
		return writePlan{}, nil, err
	}
	return writePlan{res: res, tc: tc}, pred, nil
}
