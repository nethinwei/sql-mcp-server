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
	res  entity.Resolved
	tc   Context
	full relalg.Predicate
}

func runInsert(ctx context.Context, tc Context, in createInput) (Result, error) {
	ctx, cancel := withTimeout(ctx, tc)
	defer cancel()
	tc.Transaction = in.Transaction
	plan, compiled, err := prepareInsert(ctx, tc, in)
	if err != nil {
		return Result{}, err
	}
	if tc.Dialect.Capabilities().Returning && len(plan.res.Entity.PrimaryKey()) > 0 {
		return insertWithReturning(ctx, plan.tc, plan.res, in, compiled)
	}
	return insertWithExec(ctx, plan.tc, plan.res, in, compiled)
}

func prepareInsert(ctx context.Context, tc Context, in createInput) (writePlan, codegen.Compiled, error) {
	res, err := resolveDMLEntity(tc, in.Entity)
	if err != nil {
		return writePlan{}, codegen.Compiled{}, err
	}
	tc, err = routeEntity(tc, res.Entity)
	if err != nil {
		return writePlan{}, codegen.Compiled{}, err
	}
	keys := sortedKeys(in.Values)
	if err := validateFields(res, keys...); err != nil {
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
		return Result{}, WrapDBError(err)
	}
	var row map[string]any
	for r, err := range store.Iter(rows) {
		if err != nil {
			return Result{}, WrapDBError(err)
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
		return Result{}, WrapDBError(err)
	}
	if err := afterWrite(tc, res.Entity, in.Transaction); err != nil {
		return Result{}, err
	}
	return Result{Content: []map[string]any{{"lastInsertId": r.LastInsertID, "rowsAffected": r.RowsAffected}}}, nil
}

func runUpdate(ctx context.Context, tc Context, in updateInput) (Result, error) {
	ctx, cancel := withTimeout(ctx, tc)
	defer cancel()
	tc.Transaction = in.Transaction
	plan, compiled, err := prepareUpdate(ctx, tc, in)
	if err != nil {
		return Result{}, err
	}
	compiled, err = checkGate(ctx, plan.tc, compiled)
	if err != nil {
		return Result{}, err
	}
	r, err := plan.tc.DB.ExecContext(ctx, compiled.SQL, compiled.Args...)
	if err != nil {
		return Result{}, WrapDBError(err)
	}
	if err := afterWrite(plan.tc, plan.res.Entity, in.Transaction); err != nil {
		return Result{}, err
	}
	return Result{Content: []map[string]any{{"rowsAffected": r.RowsAffected}}}, nil
}

func prepareUpdate(ctx context.Context, tc Context, in updateInput) (writePlan, codegen.Compiled, error) {
	plan, pred, err := prepareFilteredWrite(ctx, tc, in.Entity, in.Transaction, in.Filter)
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
	setItems := make([]relalg.SetItem, 0, len(in.Set))
	for _, k := range sortedKeys(in.Set) {
		setItems = append(setItems, relalg.SetItem{Field: k, Value: in.Set[k]})
	}
	target := relalg.RelationRef{Name: plan.res.Entity.Source, Schema: plan.res.Entity.Schema}
	compiled, err := codegen.Renderer{Dialect: plan.tc.Dialect}.Compile(
		relalg.Update{Target: target, Predicate: full, Set: setItems},
		codegen.WithPrimaryKey(plan.res.Entity.PrimaryKey()...),
		codegen.WithMaxINCardinality(effectiveMaxIN(plan.tc)),
	)
	if err != nil {
		return writePlan{}, codegen.Compiled{}, err
	}
	return writePlan{res: plan.res, tc: plan.tc, full: full}, compiled, nil
}

func runDelete(ctx context.Context, tc Context, in deleteInput) (Result, error) {
	ctx, cancel := withTimeout(ctx, tc)
	defer cancel()
	tc.Transaction = in.Transaction
	plan, compiled, err := prepareDelete(ctx, tc, in)
	if err != nil {
		return Result{}, err
	}
	compiled, err = checkGate(ctx, plan.tc, compiled)
	if err != nil {
		return Result{}, err
	}
	r, err := plan.tc.DB.ExecContext(ctx, compiled.SQL, compiled.Args...)
	if err != nil {
		return Result{}, WrapDBError(err)
	}
	if err := afterWrite(plan.tc, plan.res.Entity, in.Transaction); err != nil {
		return Result{}, err
	}
	return Result{Content: []map[string]any{{"rowsAffected": r.RowsAffected}}}, nil
}

func prepareDelete(ctx context.Context, tc Context, in deleteInput) (writePlan, codegen.Compiled, error) {
	plan, pred, err := prepareFilteredWrite(ctx, tc, in.Entity, in.Transaction, in.Filter)
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
	target := relalg.RelationRef{Name: plan.res.Entity.Source, Schema: plan.res.Entity.Schema}
	compiled, err := codegen.Renderer{Dialect: plan.tc.Dialect}.Compile(
		relalg.Delete{Target: target, Predicate: full},
		codegen.WithPrimaryKey(plan.res.Entity.PrimaryKey()...),
		codegen.WithMaxINCardinality(effectiveMaxIN(plan.tc)),
	)
	if err != nil {
		return writePlan{}, codegen.Compiled{}, err
	}
	return writePlan{res: plan.res, tc: plan.tc, full: full}, compiled, nil
}

func prepareFilteredWrite(
	ctx context.Context,
	tc Context,
	entityName, transaction string,
	filter []condJSON,
) (writePlan, relalg.Predicate, error) {
	tc.Transaction = transaction
	res, err := resolveDMLEntity(tc, entityName)
	if err != nil {
		return writePlan{}, nil, err
	}
	tc, err = routeEntity(tc, res.Entity)
	if err != nil {
		return writePlan{}, nil, err
	}
	pred, err := filterToPredicate(filter)
	if err != nil {
		return writePlan{}, nil, err
	}
	return writePlan{res: res, tc: tc}, pred, nil
}
