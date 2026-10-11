// Package telemetry provides the metrics of tool calls: a call counter by
// tool and outcome, a duration histogram by tool, and the audit events
// dropped, as OpenTelemetry instruments on a meter provider of their own,
// served in the Prometheus exposition format at /metrics. Hosts that need
// the metrics elsewhere read them from the Exporter's registry.
package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/nethinwei/sql-mcp-server/core/engine"
	"github.com/nethinwei/sql-mcp-server/core/hook"
	"github.com/nethinwei/sql-mcp-server/core/ratelimit"
	"github.com/nethinwei/sql-mcp-server/core/tool"
)

// durationBuckets are the histogram upper bounds in seconds.
var durationBuckets = []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// Outcome values for infrastructure rejections that have no Denial code.
const (
	outcomeOK          = "OK"
	outcomeOverloaded  = "OVERLOADED"
	outcomeRateLimited = "RATE_LIMITED"
	outcomeCircuitOpen = "CIRCUIT_OPEN"
	outcomeClosed      = "CLOSED"
	outcomeTimeout     = "TIMEOUT"
	outcomeCanceled    = "CANCELED"
	outcomeInternal    = "INTERNAL"
)

// Metrics records tool calls and serves them. The zero value is not usable;
// construct with NewMetrics.
type Metrics struct {
	provider     *sdkmetric.MeterProvider
	registry     *prometheus.Registry
	calls        metric.Int64Counter
	durations    metric.Float64Histogram
	auditDropped func() int64
}

// NewMetrics returns an empty collector. It cannot fail with the instruments
// it declares; an error would be a bug in this package.
func NewMetrics() *Metrics {
	m, err := newMetrics()
	if err != nil {
		panic("telemetry: " + err.Error())
	}
	return m
}

func newMetrics() (*Metrics, error) {
	registry := prometheus.NewRegistry()
	exporter, err := otelprom.New(
		otelprom.WithRegisterer(registry), otelprom.WithoutScopeInfo(), otelprom.WithoutTargetInfo(),
	)
	if err != nil {
		return nil, err
	}
	m := &Metrics{provider: sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter)), registry: registry}
	meter := m.provider.Meter("sql-mcp-server/telemetry")
	m.calls, err = meter.Int64Counter("sql_mcp_tool_calls",
		metric.WithDescription("Tool calls by tool and outcome (OK, denial code, or infrastructure rejection)."))
	if err != nil {
		return nil, err
	}
	if m.durations, err = meter.Float64Histogram("sql_mcp_tool_duration", metric.WithUnit("s"),
		metric.WithDescription("Tool call duration."), metric.WithExplicitBucketBoundaries(durationBuckets...)); err != nil {
		return nil, err
	}
	dropped, err := meter.Int64ObservableCounter("sql_mcp_audit_dropped",
		metric.WithDescription("Audit events dropped due to a full queue."))
	if err != nil {
		return nil, err
	}
	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		var n int64
		if m.auditDropped != nil {
			n = m.auditDropped()
		}
		o.ObserveInt64(dropped, n)
		return nil
	}, dropped)
	return m, err
}

// SetAuditDropped installs a live reader for the audit drop counter (e.g.
// audit.AsyncAuditor.Dropped), sampled at scrape time. Set it before the
// first scrape.
func (m *Metrics) SetAuditDropped(read func() int64) { m.auditDropped = read }

// Registry is the Prometheus registry the metrics are exported to, for a
// host serving them alongside its own.
func (m *Metrics) Registry() *prometheus.Registry { return m.registry }

// Shutdown flushes and stops the meter provider.
func (m *Metrics) Shutdown(ctx context.Context) error { return m.provider.Shutdown(ctx) }

type startTimeCtxKey struct{}

// Hooks returns lifecycle hooks that record one counter increment and one
// duration observation per tool call. Compose with other hooks via hook.Join.
func (m *Metrics) Hooks() *hook.Hooks {
	return &hook.Hooks{
		BeforeTool: func(ctx context.Context, _ string, _ json.RawMessage) context.Context {
			return context.WithValue(ctx, startTimeCtxKey{}, time.Now())
		},
		AfterTool: func(ctx context.Context, name string, err error) {
			var elapsed time.Duration
			if start, ok := ctx.Value(startTimeCtxKey{}).(time.Time); ok {
				elapsed = time.Since(start)
			}
			m.record(ctx, name, Outcome(err), elapsed)
		},
	}
}

func (m *Metrics) record(ctx context.Context, toolName, outcome string, elapsed time.Duration) {
	byTool := attribute.String("tool", toolName)
	m.calls.Add(ctx, 1, metric.WithAttributes(byTool, attribute.String("outcome", outcome)))
	m.durations.Record(ctx, elapsed.Seconds(), metric.WithAttributes(byTool))
}

// Outcome classifies a tool error as a stable metric label: "" error is OK,
// governed rejections use their Denial code, infrastructure rejections use
// dedicated values, and everything else is INTERNAL (details stay in logs).
func Outcome(err error) string {
	switch {
	case err == nil:
		return outcomeOK
	case errors.Is(err, engine.ErrOverloaded):
		return outcomeOverloaded
	case errors.Is(err, ratelimit.ErrRateLimited):
		return outcomeRateLimited
	case errors.Is(err, ratelimit.ErrCircuitOpen):
		return outcomeCircuitOpen
	case errors.Is(err, engine.ErrClosed):
		return outcomeClosed
	case errors.Is(err, context.DeadlineExceeded):
		return outcomeTimeout
	case errors.Is(err, context.Canceled):
		return outcomeCanceled
	}
	if d, ok := tool.DenialFor(err, ""); ok {
		return d.Code
	}
	return outcomeInternal
}

// LogHooks returns hooks that emit one structured log line per tool failure,
// carrying the decision ID so log lines correlate with the MCP response, the
// audit event, and the trace span of the same call.
func LogHooks(logger *slog.Logger) *hook.Hooks {
	return &hook.Hooks{
		OnError: func(ctx context.Context, err error) {
			logger.LogAttrs(ctx, slog.LevelWarn, "tool call failed",
				slog.String("decisionId", tool.DecisionIDFromContext(ctx)),
				slog.String("outcome", Outcome(err)),
				slog.String("error", err.Error()),
			)
		},
	}
}

// ServeHTTP writes the metrics in the Prometheus text exposition format.
func (m *Metrics) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{
		ErrorHandling: promhttp.ContinueOnError,
		ErrorLog:      promLogger{},
	}).ServeHTTP(w, r)
}

// promLogger logs the scrape errors promhttp reports through slog.
type promLogger struct{}

func (promLogger) Println(v ...any) { slog.Warn(fmt.Sprint(v...)) }
