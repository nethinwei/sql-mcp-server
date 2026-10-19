package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nethinwei/sql-mcp-server/core/codegen"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

// decodeTool reads a tool's input strictly (unknown fields fail) and
// normalizes it (JSON numbers to Go numbers); any fault is ErrInvalidInput.
func decodeTool[T any](input json.RawMessage, normalize func(*T) error) (T, error) {
	var in T
	if err := decodeInput(input, &in); err != nil {
		return in, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if normalize != nil {
		if err := normalize(&in); err != nil {
			return in, fmt.Errorf("%w: %v", ErrInvalidInput, err)
		}
	}
	return in, nil
}

// beginCall starts a tool's work on tc: bounded by the query timeout,
// metered, and in transaction (none when empty). end ends it.
func beginCall(ctx context.Context, tc Context, readOnly bool, transaction string) (context.Context, Context, func()) {
	ctx, cancel := withTimeout(ctx, tc, 0)
	tc, endMetering := meterCall(tc, readOnly)
	tc.Transaction = transaction
	return ctx, tc, func() {
		endMetering() // after the audit: the result is held until the call ends
		cancel()
	}
}

// entityPlan is a tool's work on one entity: the entity resolved, and tc
// routed to its datasource and the connection the action uses.
type entityPlan struct {
	res entity.Resolved
	tc  Context
}

// planEntity resolves the entity name selects for action, routes tc to it
// and reads the request's filter (at most the configured conditions).
func planEntity(
	ctx context.Context, tc Context, name string, action entity.Action, filter []condJSON,
) (entityPlan, relalg.Predicate, error) {
	res, err := resolveDMLEntity(tc, name)
	if err != nil {
		return entityPlan{}, nil, err
	}
	tc, err = routeEntity(ctx, tc, res.Entity, action)
	if err != nil {
		return entityPlan{}, nil, err
	}
	pred, err := filterToPredicate(filter, tc.MaxFilterConditions)
	if err != nil {
		return entityPlan{}, nil, err
	}
	if action != entity.ActionRead && action != entity.ActionAggregate && relalg.HasExists(pred) {
		return entityPlan{}, nil, fmt.Errorf("%w: exists filters reads and aggregates only", ErrInvalidInput)
	}
	pred, err = coercePredicate(res.Entity, columnsPredicate(res.Entity, pred))
	if err != nil {
		return entityPlan{}, nil, err
	}
	return entityPlan{res: res, tc: tc}, pred, nil
}

// compile renders expr for e on tc's dialect, bounded by tc's IN limit and
// knowing e's primary key; opts add to or override that.
func compile(tc Context, e entity.Entity, expr relalg.Expr, opts ...codegen.CompileOption) (codegen.Compiled, error) {
	base := []codegen.CompileOption{
		codegen.WithPrimaryKey(e.PrimaryKey()...), codegen.WithMaxINCardinality(effectiveMaxIN(tc)),
	}
	c, err := codegen.Renderer{Dialect: tc.Dialect}.Compile(expr, append(base, opts...)...)
	literalNumbers(c.Args)
	return c, err
}

// identityKeys names the keys identifying one row of e for tc's statement:
// the ones the database enforces when enforced (a write), and none that is
// deferrable inside a transaction.
func identityKeys(tc Context, e entity.Entity, enforced bool) codegen.CompileOption {
	return codegen.WithIdentityKeys(e.IdentityKeys(enforced, tc.Transaction != "")...)
}

func relationshipByName(e entity.Entity, name string) (entity.Relationship, bool) {
	for _, relation := range e.Relations {
		if relation.Name == name {
			return relation, true
		}
	}
	return entity.Relationship{}, false
}

func effectiveMaxIN(tc Context) int {
	if tc.MaxINListSize > 0 {
		return tc.MaxINListSize
	}
	return relalg.DefaultMaxINCardinality
}
