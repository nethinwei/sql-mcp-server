package hook

import (
	"context"
	"errors"
	"testing"

	"github.com/nethinwei/sql-mcp-server/cost"
	"github.com/nethinwei/sql-mcp-server/rbac"
)

func TestNilSafeNoPanic(t *testing.T) {
	t.Parallel()
	var h *Hooks
	ctx := h.FireBeforeTool(context.Background(), "x", nil)
	if ctx == nil {
		t.Fatal("ctx should be returned")
	}
	h.FireAfterTool(context.Background(), "x", nil, nil)
	h.FireOnError(context.Background(), errors.New("e"))
	h.FireCostGate(context.Background(), cost.Plan{}, cost.Score{}, "allow")
	h.FireAuthorize(context.Background(), rbac.Request{}, rbac.Decision{})
}

func TestFireInvokesCallback(t *testing.T) {
	t.Parallel()
	called := false
	h := &Hooks{OnError: func(_ context.Context, _ error) { called = true }}
	h.FireOnError(context.Background(), errors.New("e"))
	if !called {
		t.Fatal("OnError not invoked")
	}
}
