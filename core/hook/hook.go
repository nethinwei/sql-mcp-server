package hook

import (
	"context"
	"encoding/json"

	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
)

// Hooks holds optional lifecycle callbacks. Every Fire* method is nil-safe:
// a nil Hooks or nil field is a no-op.
type Hooks struct {
	BeforeTool  func(ctx context.Context, name string, input json.RawMessage) context.Context
	AfterTool   func(ctx context.Context, name string, result any, err error)
	OnError     func(ctx context.Context, err error)
	OnCostGate  func(ctx context.Context, plan cost.Plan, score cost.Score, decision string)
	OnAuthorize func(ctx context.Context, req rbac.Request, dec rbac.Decision)
}

// FireBeforeTool runs BeforeTool or returns ctx unchanged.
func (h *Hooks) FireBeforeTool(ctx context.Context, name string, input json.RawMessage) context.Context {
	if h == nil || h.BeforeTool == nil {
		return ctx
	}
	return h.BeforeTool(ctx, name, input)
}

// FireAfterTool runs AfterTool if set.
func (h *Hooks) FireAfterTool(ctx context.Context, name string, result any, err error) {
	if h == nil || h.AfterTool == nil {
		return
	}
	h.AfterTool(ctx, name, result, err)
}

// FireOnError runs OnError if set.
func (h *Hooks) FireOnError(ctx context.Context, err error) {
	if h == nil || h.OnError == nil {
		return
	}
	h.OnError(ctx, err)
}

// FireCostGate runs OnCostGate if set. decision is "allow", "soft", or "hard".
func (h *Hooks) FireCostGate(ctx context.Context, plan cost.Plan, score cost.Score, decision string) {
	if h == nil || h.OnCostGate == nil {
		return
	}
	h.OnCostGate(ctx, plan, score, decision)
}

// FireAuthorize runs OnAuthorize if set.
func (h *Hooks) FireAuthorize(ctx context.Context, req rbac.Request, dec rbac.Decision) {
	if h == nil || h.OnAuthorize == nil {
		return
	}
	h.OnAuthorize(ctx, req, dec)
}

// Join composes hooks so independent consumers (tracing, metrics, ...) can
// observe the same lifecycle. Callbacks run in argument order; BeforeTool
// threads the context through the chain. Nil entries are skipped.
func Join(hooks ...*Hooks) *Hooks {
	return &Hooks{
		BeforeTool: func(ctx context.Context, name string, input json.RawMessage) context.Context {
			for _, h := range hooks {
				ctx = h.FireBeforeTool(ctx, name, input)
			}
			return ctx
		},
		AfterTool: func(ctx context.Context, name string, result any, err error) {
			for _, h := range hooks {
				h.FireAfterTool(ctx, name, result, err)
			}
		},
		OnError: func(ctx context.Context, err error) {
			for _, h := range hooks {
				h.FireOnError(ctx, err)
			}
		},
		OnCostGate: func(ctx context.Context, plan cost.Plan, score cost.Score, decision string) {
			for _, h := range hooks {
				h.FireCostGate(ctx, plan, score, decision)
			}
		},
		OnAuthorize: func(ctx context.Context, req rbac.Request, dec rbac.Decision) {
			for _, h := range hooks {
				h.FireAuthorize(ctx, req, dec)
			}
		},
	}
}
