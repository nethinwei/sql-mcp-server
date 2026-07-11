package hook

import (
	"context"
	"encoding/json"
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

func TestJoinRunsAllHooksAndThreadsContext(t *testing.T) {
	t.Parallel()
	type ctxKey struct{}
	var order []string
	first := &Hooks{
		BeforeTool: func(ctx context.Context, _ string, _ json.RawMessage) context.Context {
			order = append(order, "first-before")
			return context.WithValue(ctx, ctxKey{}, "from-first")
		},
		OnError: func(context.Context, error) { order = append(order, "first-error") },
	}
	second := &Hooks{
		BeforeTool: func(ctx context.Context, _ string, _ json.RawMessage) context.Context {
			order = append(order, "second-before")
			if ctx.Value(ctxKey{}) != "from-first" {
				t.Error("context from the first hook was not threaded through")
			}
			return ctx
		},
		OnError: func(context.Context, error) { order = append(order, "second-error") },
	}
	joined := Join(first, nil, second)
	ctx := joined.FireBeforeTool(context.Background(), "x", nil)
	if ctx.Value(ctxKey{}) != "from-first" {
		t.Fatal("joined BeforeTool must return the threaded context")
	}
	joined.FireOnError(ctx, errors.New("e"))
	want := []string{"first-before", "second-before", "first-error", "second-error"}
	if len(order) != len(want) {
		t.Fatalf("order = %v", order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
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
