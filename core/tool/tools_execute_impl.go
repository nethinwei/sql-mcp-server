package tool

import (
	"context"
	"fmt"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

func runExecute(ctx context.Context, tc Context, in executeInput, requireDMLTools bool) (Result, error) {
	ctx, tc, end := beginCall(ctx, tc, false, in.Transaction)
	defer end()
	res, tc, g, err := prepareExecute(ctx, tc, in, requireDMLTools)
	if err != nil {
		return Result{}, err
	}
	sql, err := compileExecute(ctx, tc, res, in)
	if err != nil {
		return Result{}, err
	}
	st := newStatement(tc, res, g, sql)
	st.write, st.fields, st.rowCap = true, allowedProcedureFields(res, g.Decision), tc.MaxProcedureRows
	got, err := st.query(ctx)
	if err != nil {
		return Result{}, err
	}
	return Result{Content: got.rows, ReturnedRows: int64(len(got.rows))}, nil
}

func prepareExecute(
	ctx context.Context,
	tc Context,
	in executeInput,
	requireDMLTools bool,
) (entity.Resolved, Context, grant, error) {
	res, ok := tc.Registry.Resolve(in.Entity)
	if !ok {
		return entity.Resolved{}, tc, grant{}, ErrEntityNotFound
	}
	if requireDMLTools && !res.Entity.MCP.DMLTools {
		return entity.Resolved{}, tc, grant{}, ErrDMLToolsDisabled
	}
	if res.Entity.Kind != entity.KindProcedure {
		return entity.Resolved{}, tc, grant{}, fmt.Errorf(
			"%w: %q is not a procedure",
			ErrInvalidInput,
			in.Entity,
		)
	}
	if !res.Entity.MCP.TrustedProcedure {
		return entity.Resolved{}, tc, grant{}, ErrUnauthorized
	}
	routed, err := routeEntity(ctx, tc, res.Entity, entity.ActionExecute)
	if err != nil {
		return entity.Resolved{}, tc, grant{}, err
	}
	g, err := authorizeRequest(ctx, routed, rbac.Request{
		Role: routed.Role, Subject: routed.Subject, Entity: in.Entity, Action: entity.ActionExecute,
	})
	if err != nil {
		return entity.Resolved{}, routed, grant{}, err
	}
	return res, routed, g, nil
}

func compileExecute(ctx context.Context, tc Context, res entity.Resolved, in executeInput) (gated, error) {
	args, err := orderedProcArgs(res.Entity.Params, in.Args)
	if err != nil {
		return gated{}, err
	}
	compiled, err := compile(tc, res.Entity, relalg.Call{Procedure: res.Entity.Ref(), Args: args})
	if err != nil {
		return gated{}, err
	}
	return gateSQL(ctx, tc, compiled)
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
