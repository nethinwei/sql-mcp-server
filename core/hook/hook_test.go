package hook

import (
	"context"
	"errors"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
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

func TestFireCostGateAndAuthorize(t *testing.T) {
	t.Parallel()
	gate := false
	h := &Hooks{OnCostGate: func(_ context.Context, _ cost.Plan, _ cost.Score, _ string) { gate = true }}
	h.FireCostGate(context.Background(), cost.Plan{}, cost.Score{}, "allow")
	if !gate {
		t.Fatal("OnCostGate not invoked")
	}
	auth := false
	h2 := &Hooks{OnAuthorize: func(_ context.Context, _ rbac.Request, _ rbac.Decision) { auth = true }}
	h2.FireAuthorize(context.Background(), rbac.Request{}, rbac.Decision{})
	if !auth {
		t.Fatal("OnAuthorize not invoked")
	}
}
