// Package telemetry provides the minimal metrics set for tool calls: a call
// counter labeled by tool and outcome, a per-tool duration histogram, and an
// audit-drop counter, served in Prometheus text exposition format. It is
// dependency-free by design; hosts that need a full metrics stack can mount
// their own handler instead.
package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/engine"
	"github.com/nethinwei/sql-mcp-server/core/hook"
	"github.com/nethinwei/sql-mcp-server/core/ratelimit"
	"github.com/nethinwei/sql-mcp-server/core/tool"
)

// durationBuckets are the histogram upper bounds in seconds.
var durationBuckets = [...]float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

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

type callKey struct {
	tool    string
	outcome string
}

type histogram struct {
	counts [len(durationBuckets) + 1]uint64 // +1 for +Inf
	sum    float64
	total  uint64
}

func (h *histogram) observe(seconds float64) {
	h.sum += seconds
	h.total++
	for i, bound := range durationBuckets {
		if seconds <= bound {
			h.counts[i]++
			return
		}
	}
	h.counts[len(durationBuckets)]++
}

// Metrics is a concurrency-safe collector plus http.Handler. The zero value
// is not usable; construct with NewMetrics.
type Metrics struct {
	mu           sync.Mutex
	calls        map[callKey]uint64
	durations    map[string]*histogram
	auditDropped func() int64
}

// NewMetrics returns an empty collector.
func NewMetrics() *Metrics {
	return &Metrics{
		calls:     map[callKey]uint64{},
		durations: map[string]*histogram{},
	}
}

// SetAuditDropped installs a live reader for the audit drop counter (e.g.
// audit.AsyncAuditor.Dropped), sampled at scrape time.
func (m *Metrics) SetAuditDropped(read func() int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.auditDropped = read
}

type startTimeCtxKey struct{}

// Hooks returns lifecycle hooks that record one counter increment and one
// duration observation per tool call. Compose with other hooks via hook.Join.
func (m *Metrics) Hooks() *hook.Hooks {
	return &hook.Hooks{
		BeforeTool: func(ctx context.Context, _ string, _ json.RawMessage) context.Context {
			return context.WithValue(ctx, startTimeCtxKey{}, time.Now())
		},
		AfterTool: func(ctx context.Context, name string, _ any, err error) {
			var elapsed time.Duration
			if start, ok := ctx.Value(startTimeCtxKey{}).(time.Time); ok {
				elapsed = time.Since(start)
			}
			m.record(name, Outcome(err), elapsed)
		},
	}
}

func (m *Metrics) record(toolName, outcome string, elapsed time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls[callKey{tool: toolName, outcome: outcome}]++
	h := m.durations[toolName]
	if h == nil {
		h = &histogram{}
		m.durations[toolName] = h
	}
	h.observe(elapsed.Seconds())
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

// ServeHTTP writes the Prometheus text exposition format. Output is sorted
// so scrapes are deterministic.
func (m *Metrics) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	var b strings.Builder
	m.writeCalls(&b)
	m.writeDurations(&b)
	m.writeAuditDropped(&b)
	_, _ = w.Write([]byte(b.String()))
}

func (m *Metrics) writeCalls(b *strings.Builder) {
	b.WriteString(
		"# HELP sql_mcp_tool_calls_total Tool calls by tool and outcome (OK, denial code, or infrastructure rejection).\n",
	)
	b.WriteString("# TYPE sql_mcp_tool_calls_total counter\n")
	keys := make([]callKey, 0, len(m.calls))
	for k := range m.calls {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].tool != keys[j].tool {
			return keys[i].tool < keys[j].tool
		}
		return keys[i].outcome < keys[j].outcome
	})
	for _, k := range keys {
		fmt.Fprintf(b, "sql_mcp_tool_calls_total{tool=%q,outcome=%q} %d\n", k.tool, k.outcome, m.calls[k])
	}
}

func (m *Metrics) writeDurations(b *strings.Builder) {
	b.WriteString("# HELP sql_mcp_tool_duration_seconds Tool call duration.\n")
	b.WriteString("# TYPE sql_mcp_tool_duration_seconds histogram\n")
	tools := make([]string, 0, len(m.durations))
	for name := range m.durations {
		tools = append(tools, name)
	}
	sort.Strings(tools)
	for _, name := range tools {
		h := m.durations[name]
		cumulative := uint64(0)
		for i, bound := range durationBuckets {
			cumulative += h.counts[i]
			fmt.Fprintf(b, "sql_mcp_tool_duration_seconds_bucket{tool=%q,le=%q} %d\n",
				name, formatBound(bound), cumulative)
		}
		fmt.Fprintf(b, "sql_mcp_tool_duration_seconds_bucket{tool=%q,le=\"+Inf\"} %d\n", name, h.total)
		fmt.Fprintf(b, "sql_mcp_tool_duration_seconds_sum{tool=%q} %g\n", name, h.sum)
		fmt.Fprintf(b, "sql_mcp_tool_duration_seconds_count{tool=%q} %d\n", name, h.total)
	}
}

func (m *Metrics) writeAuditDropped(b *strings.Builder) {
	b.WriteString("# HELP sql_mcp_audit_dropped_total Audit events dropped due to a full queue.\n")
	b.WriteString("# TYPE sql_mcp_audit_dropped_total counter\n")
	dropped := int64(0)
	if m.auditDropped != nil {
		dropped = m.auditDropped()
	}
	fmt.Fprintf(b, "sql_mcp_audit_dropped_total %d\n", dropped)
}

func formatBound(bound float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", bound), "0"), ".")
}
