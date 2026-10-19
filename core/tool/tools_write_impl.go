package tool

import (
	"context"
	"fmt"

	"github.com/nethinwei/sql-mcp-server/core/codegen"
	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

func runInsert(ctx context.Context, tc Context, in createInput) (Result, error) {
	ctx, tc, end := beginCall(ctx, tc, false, in.Transaction)
	defer end()
	st, err := prepareInsert(ctx, tc, in)
	if err != nil {
		return Result{}, err
	}
	if st.tc.Dialect.Capabilities().Enforced(dialect.Returning) && len(st.res.Entity.PrimaryKey()) > 0 {
		return insertWithReturning(ctx, st)
	}
	return insertWithExec(ctx, st, in)
}

func prepareInsert(ctx context.Context, tc Context, in createInput) (statement, error) {
	plan, _, err := planEntity(ctx, tc, in.Entity, entity.ActionCreate, nil)
	if err != nil {
		return statement{}, err
	}
	res, tc := plan.res, plan.tc
	keys := sortedKeys(in.Values)
	if err := validateFields(res, keys...); err != nil {
		return statement{}, err
	}
	if err := validateWritable(res, keys...); err != nil {
		return statement{}, err
	}
	if err := distinctColumns(res.Entity, keys); err != nil {
		return statement{}, err
	}
	g, err := authorizeRequest(ctx, tc, rbac.Request{
		Role: tc.Role, Subject: tc.Subject, Entity: in.Entity, Action: entity.ActionCreate,
		WriteFields: keys,
	})
	if err != nil {
		return statement{}, err
	}
	columns, tup, err := insertColumns(res.Entity, g.RowFilter, in.Values)
	if err != nil {
		return statement{}, err
	}
	compiled, err := compile(
		tc,
		res.Entity,
		relalg.Insert{Target: res.Entity.Ref(), Columns: columns, Tuples: []relalg.Tuple{tup}},
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
	if got.truncated {
		return Result{Content: []map[string]any{{"rowsAffected": int64(1), "truncated": true}}, Truncated: true}, nil
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
	ctx, tc, end := beginCall(ctx, tc, false, transaction)
	defer end()
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
	if len(in.Set) == 0 {
		return statement{}, fmt.Errorf("%w: set assigns no field", ErrInvalidInput)
	}
	plan, pred, err := planEntity(ctx, tc, in.Entity, entity.ActionUpdate, in.Filter)
	if err != nil {
		return statement{}, err
	}
	if err := validateUpdateFields(plan.res, in); err != nil {
		return statement{}, err
	}
	g, err := authorizeRequest(ctx, plan.tc, rbac.Request{
		Role: plan.tc.Role, Subject: plan.tc.Subject, Entity: in.Entity, Action: entity.ActionUpdate,
		ReadFields: filterFields(in.Filter), WriteFields: sortedKeys(in.Set), Predicate: pred,
	})
	if err != nil {
		return statement{}, err
	}
	if err := refuseKeyUpdate(plan.res.Entity, sortedKeys(in.Set)); err != nil {
		return statement{}, err
	}
	full := andPreds(pred, g.RowFilter)
	if full == nil {
		return statement{}, ErrUnsafeWrite
	}
	check, err := updateCheck(plan.res.Entity, g.RowFilter, in.Set)
	if err != nil {
		return statement{}, err
	}
	full = andPreds(full, check)
	if err := checkCascades(ctx, plan.tc, plan.res.Entity, entity.ActionUpdate, sortedKeys(in.Set)); err != nil {
		return statement{}, err
	}
	setItems := make([]relalg.SetItem, 0, len(in.Set))
	for _, k := range sortedKeys(in.Set) {
		setItems = append(setItems, relalg.SetItem{Field: columnOf(plan.res.Entity, k), Value: in.Set[k]})
	}
	compiled, err := compile(
		plan.tc,
		plan.res.Entity,
		relalg.Update{Target: plan.res.Entity.Ref(), Predicate: full, Set: setItems},
		identityKeys(plan.tc, plan.res.Entity, true),
	)
	if err != nil {
		return statement{}, err
	}
	return plan.statement(ctx, g, compiled)
}

func validateUpdateFields(res entity.Resolved, in updateInput) error {
	if err := validateFields(res, append(filterFields(in.Filter), sortedKeys(in.Set)...)...); err != nil {
		return err
	}
	if err := validateUnmaskedFields(res, filterFields(in.Filter)...); err != nil {
		return err
	}
	if err := distinctColumns(res.Entity, sortedKeys(in.Set)); err != nil {
		return err
	}
	return validateWritable(res, sortedKeys(in.Set)...)
}

func prepareDelete(ctx context.Context, tc Context, in deleteInput) (statement, error) {
	plan, pred, err := planEntity(ctx, tc, in.Entity, entity.ActionDelete, in.Filter)
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
	compiled, err := compile(plan.tc, plan.res.Entity, relalg.Delete{Target: plan.res.Entity.Ref(), Predicate: full},
		identityKeys(plan.tc, plan.res.Entity, true))
	if err != nil {
		return statement{}, err
	}
	return plan.statement(ctx, g, compiled)
}

// statement gates compiled, the write of the plan.
func (p entityPlan) statement(ctx context.Context, g grant, compiled codegen.Compiled) (statement, error) {
	sql, err := gateSQL(ctx, p.tc, compiled)
	if err != nil {
		return statement{}, err
	}
	st := newStatement(p.tc, p.res, g, sql)
	st.write = true
	return st, nil
}
