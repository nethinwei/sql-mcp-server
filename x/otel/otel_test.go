package otel

import (
	"context"
	"errors"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
)

func TestNewHooksNoPanicWithoutProvider(t *testing.T) {
	t.Parallel()
	h := NewHooks()
	if h == nil {
		t.Fatal("nil hooks")
	}
	// With no tracer provider configured, otel uses a no-op tracer; callbacks
	// must still be safe to invoke.
	ctx := h.FireBeforeTool(context.Background(), "read_records", nil)
	h.FireAfterTool(ctx, "read_records", nil, nil)
	h.FireOnError(ctx, errors.New("e"))
	h.FireCostGate(ctx, cost.Plan{}, cost.Score{}, "allow")
	h.FireAuthorize(ctx, rbac.Request{Role: "reader", Entity: "users"}, rbac.Decision{})
}
