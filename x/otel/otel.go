package otel

import (
	"context"
	"encoding/json"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/nethinwei/sql-mcp-server/cost"
	"github.com/nethinwei/sql-mcp-server/hook"
	"github.com/nethinwei/sql-mcp-server/rbac"
)

// NewHooks returns hook.Hooks that emit OpenTelemetry spans for tool calls and
// record errors and gate/authorize decisions as span attributes. It uses the
// global tracer provider; initialize one (e.g. an OTLP exporter) in main for
// spans to be exported. Without a provider, otel uses a no-op tracer.
func NewHooks() *hook.Hooks {
	tracer := otel.Tracer("sql-mcp-server")
	return &hook.Hooks{
		BeforeTool: func(ctx context.Context, name string, _ json.RawMessage) context.Context {
			ctx, _ = tracer.Start(ctx, "tool:"+name)
			return ctx
		},
		AfterTool: func(ctx context.Context, _ string, _ any, _ error) {
			trace.SpanFromContext(ctx).End()
		},
		OnError: func(ctx context.Context, err error) {
			trace.SpanFromContext(ctx).RecordError(err)
		},
		OnCostGate: func(ctx context.Context, _ cost.Plan, _ cost.Score, decision string) {
			trace.SpanFromContext(ctx).SetAttributes(attribute.String("cost.gate.decision", decision))
		},
		OnAuthorize: func(ctx context.Context, req rbac.Request, _ rbac.Decision) {
			trace.SpanFromContext(ctx).SetAttributes(
				attribute.String("rbac.entity", req.Entity),
				attribute.String("rbac.role", req.Role),
			)
		},
	}
}
