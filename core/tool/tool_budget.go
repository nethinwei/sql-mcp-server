package tool

import (
	"context"

	"github.com/nethinwei/sql-mcp-server/core/budget"
	"github.com/nethinwei/sql-mcp-server/core/entity"
)

func acquireToolBudget(ctx context.Context, action string, tc Context) (budget.Lease, context.Context, Context, error) {
	if tc.Budget == nil {
		return nil, ctx, tc, nil
	}
	scope := budget.Scope{
		Role: tc.Role, Tenant: tenantKey(tc.Subject), Session: tc.Session,
	}
	lease, err := tc.Budget.AcquireWithReservation(ctx, scope, budget.Reservation{Cost: budgetReservation(action, tc)})
	if err != nil {
		return nil, ctx, tc, err
	}
	ctx = lease.Context()
	tc.BudgetLimits = lease.Limits()
	return lease, ctx, tc, nil
}

// budgetReservation is the session cost held while a call runs: its row cap
// for row-returning actions, otherwise 1.
func budgetReservation(action string, tc Context) int64 {
	reserved := int64(1)
	switch action {
	case entity.ActionRead.String(), entity.ActionAggregate.String():
		reserved = tc.MaxRows
	case entity.ActionExecute.String():
		reserved = tc.MaxProcedureRows
	case ActionSQL:
		reserved = tc.MaxRows
	}
	if reserved <= 0 {
		return 1
	}
	return reserved
}
