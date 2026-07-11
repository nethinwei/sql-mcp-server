package telemetry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/budget"
	"github.com/nethinwei/sql-mcp-server/core/engine"
	"github.com/nethinwei/sql-mcp-server/core/ratelimit"
	"github.com/nethinwei/sql-mcp-server/core/tool"
)

func TestMetricsHooksRecordCallsAndDurations(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	hooks := m.Hooks()
	ctx := hooks.FireBeforeTool(context.Background(), "read_records", nil)
	hooks.FireAfterTool(ctx, "read_records", nil, nil)
	ctx = hooks.FireBeforeTool(context.Background(), "read_records", nil)
	hooks.FireAfterTool(ctx, "read_records", nil, tool.ErrUnauthorized)

	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{
		`sql_mcp_tool_calls_total{tool="read_records",outcome="OK"} 1`,
		`sql_mcp_tool_calls_total{tool="read_records",outcome="UNAUTHORIZED"} 1`,
		`sql_mcp_tool_duration_seconds_count{tool="read_records"} 2`,
		"sql_mcp_audit_dropped_total 0",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics output missing %q:\n%s", want, body)
		}
	}
}

func TestMetricsAuditDroppedReadsLiveValue(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	dropped := int64(0)
	m.SetAuditDropped(func() int64 { return dropped })
	dropped = 7
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "sql_mcp_audit_dropped_total 7") {
		t.Fatalf("metrics output = %s", rec.Body.String())
	}
}

func TestOutcomeClassification(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err  error
		want string
	}{
		{nil, "OK"},
		{tool.ErrUnauthorized, "UNAUTHORIZED"},
		{fmt.Errorf("wrap: %w", budget.ErrExceeded), "BUDGET_EXCEEDED"},
		{engine.ErrOverloaded, "OVERLOADED"},
		{ratelimit.ErrRateLimited, "RATE_LIMITED"},
		{ratelimit.ErrCircuitOpen, "CIRCUIT_OPEN"},
		{engine.ErrClosed, "CLOSED"},
		{context.DeadlineExceeded, "TIMEOUT"},
		{context.Canceled, "CANCELED"},
		{errors.New("driver exploded"), "INTERNAL"},
	}
	for _, tc := range cases {
		if got := Outcome(tc.err); got != tc.want {
			t.Errorf("Outcome(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestLogHooksCarryDecisionID(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	hooks := LogHooks(slog.New(slog.NewJSONHandler(&buf, nil)))
	ctx := tool.WithDecisionID(context.Background(), "abc123")
	hooks.FireOnError(ctx, tool.ErrUnauthorized)
	line := buf.String()
	for _, want := range []string{`"decisionId":"abc123"`, `"outcome":"UNAUTHORIZED"`} {
		if !strings.Contains(line, want) {
			t.Fatalf("log line missing %q: %s", want, line)
		}
	}
}
