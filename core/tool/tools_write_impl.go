package tool

import (
	"context"

	"github.com/nethinwei/sql-mcp-server/core/codegen"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
	"github.com/nethinwei/sql-mcp-server/core/store"
)

type writePlan struct {
	res entity.Resolved
	tc  Context
}

func runInsert(ctx context.Context, tc Context, in createInput) (Result, error) {
	ctx, cancel := withTimeout(ctx, tc, 0)
	defer cancel()
	tc.Transaction = in.Transaction
	plan, compiled, err := prepareInsert(ctx, tc, in)
	if err != nil {
		return Result{}, err
	}
	if plan.tc.Dialect.Capabilities().Returning && len(plan.res.Entity.PrimaryKey()) > 0 {
		return insertWithReturning(ctx, plan.tc, plan.res, in, compiled)
	}
	return insertWithExec(ctx, plan.tc, plan.res, in, compiled)
}

func prepareInsert(ctx context.Context, tc Context, in createInput) (writePlan, codegen.Compiled, error) {
	res, err := resolveDMLEntity(tc, in.Entity)
	if err != nil {
		return writePlan{}, codegen.Compiled{}, err
	}
	tc, err = routeEntity(tc, res.Entity, entity.ActionCreate)
	if err != nil {
		return writePlan{}, codegen.Compiled{}, err
	}
	keys := sortedKeys(in.Values)
	if err := validateFields(res, keys...); err != nil {
		return writePlan{}, codegen.Compiled{}, err
	}
	if err := validateWritable(res, keys...); err != nil {
		return writePlan{}, codegen.Compiled{}, err
	}
	dec, err := authorize(ctx, tc, rbac.Request{
		Role: tc.Role, Subject: tc.Subject, Entity: in.Entity, Action: entity.ActionCreate,
		WriteFields: keys,
	})
	if err != nil {
		return writePlan{}, codegen.Compiled{}, err
	}
	if !dec.Allowed {
		return writePlan{}, codegen.Compiled{}, denyUnauthorized(dec)
	}
	cols := make([]string, len(keys))
	tup := make(relalg.Tuple, len(keys))
	for i, k := range keys {
		cols[i] = k
		tup[i] = in.Values[k]
	}
	target := relalg.RelationRef{Name: res.Entity.Source, Schema: res.Entity.Schema}
	compiled, err := codegen.Renderer{Dialect: tc.Dialect}.Compile(
		relalg.Insert{Target: target, Columns: cols, Tuples: []relalg.Tuple{tup}},
		codegen.WithPrimaryKey(res.Entity.PrimaryKey()...),
		codegen.WithMaxINCardinality(effectiveMaxIN(tc)),
	)
	if err != nil {
		return writePlan{}, codegen.Compiled{}, err
	}
	return writePlan{res: res, tc: tc}, compiled, nil
}

func insertWithReturning(
	ctx context.Context,
	tc Context,
	res entity.Resolved,
	in createInput,
	compiled codegen.Compiled,
) (Result, error) {
	rows, err := tc.DB.QueryContext(ctx, compiled.SQL, compiled.Args...)
	if err != nil {
		return Result{}, writeError(res, err)
	}
	var row map[string]any
	for r, err := range store.Iter(rows) {
		if err != nil {
			return Result{}, writeError(res, err)
		}
		row = r
		break
	}
	if err := afterWrite(tc, res.Entity, in.Transaction); err != nil {
		return Result{}, err
	}
	if row == nil {
		return Result{Content: []map[string]any{{"rowsAffected": int64(0)}}}, nil
	}
	row["rowsAffected"] = int64(1)
	if tc.Masker != nil {
		maskRow(tc.Masker, res.Entity.Attributes, row)
	}
	return Result{Content: []map[string]any{row}}, nil
}

func insertWithExec(
	ctx context.Context,
	tc Context,
	res entity.Resolved,
	in createInput,
	compiled codegen.Compiled,
) (Result, error) {
	r, err := tc.DB.ExecContext(ctx, compiled.SQL, compiled.Args...)
	if err != nil {
		return Result{}, writeError(res, err)
	}
	if err := afterWrite(tc, res.Entity, in.Transaction); err != nil {
		return Result{}, err
	}
	row := insertedKey(res.Entity, in.Values, r.LastInsertID)
	row["lastInsertId"], row["rowsAffected"] = r.LastInsertID, r.RowsAffected
	if tc.Masker != nil {
		maskRow(tc.Masker, res.Entity.Attributes, row)
	}
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

// runFilteredWrite gates and executes a filtered UPDATE or DELETE built by
// prepare, then invalidates the entity's cached reads.
func runFilteredWrite(
	ctx context.Context,
	tc Context,
	transaction string,
	prepare func(context.Context, Context) (writePlan, codegen.Compiled, error),
) (Result, error) {
	ctx, cancel := withTimeout(ctx, tc, 0)
	defer cancel()
	tc.Transaction = transaction
	plan, compiled, err := prepare(ctx, tc)
	if err != nil {
		return Result{}, err
	}
	compiled, err = checkGate(ctx, plan.tc, compiled)
	if err != nil {
		return Result{}, err
	}
	r, err := plan.tc.DB.ExecContext(ctx, compiled.SQL, compiled.Args...)
	if err != nil {
		return Result{}, writeError(plan.res, err)
	}
	if err := afterWrite(plan.tc, plan.res.Entity, transaction); err != nil {
		return Result{}, err
	}
	return Result{Content: []map[string]any{{"rowsAffected": r.RowsAffected}}}, nil
}

func prepareUpdate(ctx context.Context, tc Context, in updateInput) (writePlan, codegen.Compiled, error) {
	plan, pred, err := prepareFilteredWrite(ctx, tc, in.Entity, entity.ActionUpdate, in.Filter)
	if err != nil {
		return writePlan{}, codegen.Compiled{}, err
	}
	updFields := append(filterFields(in.Filter), sortedKeys(in.Set)...)
	if err := validateFields(plan.res, updFields...); err != nil {
		return writePlan{}, codegen.Compiled{}, err
	}
	if err := validateUnmaskedFields(plan.res, filterFields(in.Filter)...); err != nil {
		return writePlan{}, codegen.Compiled{}, err
	}
	if err := validateWritable(plan.res, sortedKeys(in.Set)...); err != nil {
		return writePlan{}, codegen.Compiled{}, err
	}
	dec, err := authorize(ctx, plan.tc, rbac.Request{
		Role: plan.tc.Role, Subject: plan.tc.Subject, Entity: in.Entity, Action: entity.ActionUpdate,
		ReadFields: filterFields(in.Filter), WriteFields: sortedKeys(in.Set), Predicate: pred,
	})
	if err != nil {
		return writePlan{}, codegen.Compiled{}, err
	}
	if !dec.Allowed {
		return writePlan{}, codegen.Compiled{}, denyUnauthorized(dec)
	}
	full := andPreds(pred, dec.RowFilter)
	if full == nil {
		return writePlan{}, codegen.Compiled{}, ErrUnsafeWrite
	}
	if err := checkCascades(ctx, plan.tc, plan.res.Entity, entity.ActionUpdate, sortedKeys(in.Set)); err != nil {
		return writePlan{}, codegen.Compiled{}, err
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
		return writePlan{}, codegen.Compiled{}, err
	}
	return plan, compiled, nil
}

func prepareDelete(ctx context.Context, tc Context, in deleteInput) (writePlan, codegen.Compiled, error) {
	plan, pred, err := prepareFilteredWrite(ctx, tc, in.Entity, entity.ActionDelete, in.Filter)
	if err != nil {
		return writePlan{}, codegen.Compiled{}, err
	}
	if err := validateFields(plan.res, filterFields(in.Filter)...); err != nil {
		return writePlan{}, codegen.Compiled{}, err
	}
	if err := validateUnmaskedFields(plan.res, filterFields(in.Filter)...); err != nil {
		return writePlan{}, codegen.Compiled{}, err
	}
	dec, err := authorize(ctx, plan.tc, rbac.Request{
		Role: plan.tc.Role, Subject: plan.tc.Subject, Entity: in.Entity, Action: entity.ActionDelete,
		ReadFields: filterFields(in.Filter), Predicate: pred,
	})
	if err != nil {
		return writePlan{}, codegen.Compiled{}, err
	}
	if !dec.Allowed {
		return writePlan{}, codegen.Compiled{}, denyUnauthorized(dec)
	}
	full := andPreds(pred, dec.RowFilter)
	if full == nil {
		return writePlan{}, codegen.Compiled{}, ErrUnsafeWrite
	}
	if err := checkCascades(ctx, plan.tc, plan.res.Entity, entity.ActionDelete, nil); err != nil {
		return writePlan{}, codegen.Compiled{}, err
	}
	target := relalg.RelationRef{Name: plan.res.Entity.Source, Schema: plan.res.Entity.Schema}
	compiled, err := codegen.Renderer{Dialect: plan.tc.Dialect}.Compile(
		relalg.Delete{Target: target, Predicate: full},
		codegen.WithPrimaryKey(plan.res.Entity.PrimaryKey()...),
		codegen.WithIdentityKeys(plan.res.Entity.IdentityKeys(true, plan.tc.Transaction != "")...),
		codegen.WithMaxINCardinality(effectiveMaxIN(plan.tc)),
	)
	if err != nil {
		return writePlan{}, codegen.Compiled{}, err
	}
	return plan, compiled, nil
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
	tc, err = routeEntity(tc, res.Entity, action)
	if err != nil {
		return writePlan{}, nil, err
	}
	pred, err := filterToPredicate(filter, tc.MaxFilterConditions)
	if err != nil {
		return writePlan{}, nil, err
	}
	return writePlan{res: res, tc: tc}, pred, nil
}
