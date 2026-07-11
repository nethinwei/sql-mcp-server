package tool

import (
	"context"
	"fmt"

	"github.com/nethinwei/sql-mcp-server/core/budget"
	"github.com/nethinwei/sql-mcp-server/core/codegen"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
	"github.com/nethinwei/sql-mcp-server/core/store"
)

func runExecute(ctx context.Context, tc Context, in executeInput, requireDMLTools bool) (Result, error) {
	ctx, cancel := withTimeout(ctx, tc)
	defer cancel()
	tc.Transaction = in.Transaction
	res, tc, dec, err := prepareExecute(ctx, tc, in, requireDMLTools)
	if err != nil {
		return Result{}, err
	}
	compiled, err := compileExecute(ctx, tc, res, in)
	if err != nil {
		return Result{}, err
	}
	out, err := collectProcedureRows(ctx, tc, res, dec, compiled)
	if err != nil {
		return Result{}, err
	}
	if err := afterWrite(tc, res.Entity, in.Transaction); err != nil {
		return Result{}, err
	}
	return Result{Content: out, ReturnedRows: int64(len(out))}, nil
}

func prepareExecute(
	ctx context.Context,
	tc Context,
	in executeInput,
	requireDMLTools bool,
) (entity.Resolved, Context, rbac.Decision, error) {
	res, ok := tc.Registry.Resolve(in.Entity)
	if !ok {
		return entity.Resolved{}, tc, rbac.Decision{}, ErrEntityNotFound
	}
	if requireDMLTools && !res.Entity.MCP.DMLTools {
		return entity.Resolved{}, tc, rbac.Decision{}, ErrDMLToolsDisabled
	}
	if res.Entity.Kind != entity.KindProcedure {
		return entity.Resolved{}, tc, rbac.Decision{}, fmt.Errorf("%w: %q is not a procedure", ErrInvalidInput, in.Entity)
	}
	if !res.Entity.MCP.TrustedProcedure {
		return entity.Resolved{}, tc, rbac.Decision{}, ErrUnauthorized
	}
	routed, err := routeEntity(tc, res.Entity)
	if err != nil {
		return entity.Resolved{}, tc, rbac.Decision{}, err
	}
	dec, err := authorize(ctx, routed, rbac.Request{
		Role: routed.Role, Subject: routed.Subject, Entity: in.Entity, Action: entity.ActionExecute,
	})
	if err != nil {
		return entity.Resolved{}, routed, rbac.Decision{}, err
	}
	if !dec.Allowed {
		return entity.Resolved{}, routed, rbac.Decision{}, ErrUnauthorized
	}
	return res, routed, dec, nil
}

func compileExecute(ctx context.Context, tc Context, res entity.Resolved, in executeInput) (codegen.Compiled, error) {
	args, err := orderedProcArgs(res.Entity.Params, in.Args)
	if err != nil {
		return codegen.Compiled{}, err
	}
	expr := relalg.Call{
		Procedure: relalg.RelationRef{Name: res.Entity.Source, Schema: res.Entity.Schema},
		Args:      args,
	}
	compiled, err := codegen.Renderer{Dialect: tc.Dialect}.Compile(
		expr,
		codegen.WithMaxINCardinality(effectiveMaxIN(tc)),
	)
	if err != nil {
		return codegen.Compiled{}, err
	}
	return checkGate(ctx, tc, compiled)
}

func collectProcedureRows(
	ctx context.Context,
	tc Context,
	res entity.Resolved,
	dec rbac.Decision,
	compiled codegen.Compiled,
) ([]map[string]any, error) {
	rows, err := tc.DB.QueryContext(ctx, compiled.SQL, compiled.Args...)
	if err != nil {
		return nil, WrapDBError(err)
	}
	defer func() { _ = rows.Close() }()
	allowedFields := allowedProcedureFields(res, dec)
	out := make([]map[string]any, 0)
	for row, err := range store.Iter(rows) {
		if err != nil {
			return nil, WrapDBError(err)
		}
		filterProcedureRow(row, allowedFields)
		if tc.Masker != nil {
			maskRow(tc.Masker, res.Attributes, row)
		}
		nextRows := int64(len(out) + 1)
		if tc.MaxProcedureRows > 0 && nextRows > tc.MaxProcedureRows {
			return nil, budget.ErrExceeded
		}
		if tc.BudgetLimits.MaxReturnedRows > 0 && nextRows > tc.BudgetLimits.MaxReturnedRows {
			return nil, budget.ErrExceeded
		}
		out = append(out, row)
	}
	return out, nil
}

func allowedProcedureFields(res entity.Resolved, dec rbac.Decision) map[string]bool {
	allowedFields := make(map[string]bool, len(dec.Fields))
	for _, field := range dec.Fields {
		if attr, ok := res.Entity.AttributeByName(field); ok && !attr.Excluded {
			allowedFields[attr.Name] = true
			if attr.Alias != "" {
				allowedFields[attr.Alias] = true
			}
		}
	}
	return allowedFields
}

func filterProcedureRow(row map[string]any, allowedFields map[string]bool) {
	for field := range row {
		if !allowedFields[field] {
			delete(row, field)
		}
	}
}
