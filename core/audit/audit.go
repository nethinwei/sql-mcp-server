package audit

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/bus"
	"github.com/nethinwei/sql-mcp-server/core/cost"
)

// ErrSinkClosed is returned by Record on a closed AsyncAuditor.
var ErrSinkClosed = errors.New("audit: sink closed")

// Event records one tool invocation for compliance. DecisionID correlates the
// event with the MCP response and trace span of the same call.
//
// The JSON field names below are a frozen public contract (see
// docs/tool-contract.md): adding a new optional field is compatible; renaming
// or removing a field is breaking. Duration serializes as integer
// milliseconds under "durationMs". Cost is reserved: its inner structure is
// not yet part of the frozen contract.
type Event struct {
	Time       time.Time `json:"time"`
	DecisionID string    `json:"decisionId,omitempty"`
	Role       string    `json:"role,omitempty"`
	// User and Roles identify a configured user and its roles; Grants lists
	// the covering grant IDs of the authorization decisions.
	User          string          `json:"user,omitempty"`
	Roles         []string        `json:"roles,omitempty"`
	Grants        []string        `json:"grants,omitempty"`
	Entity        string          `json:"entity,omitempty"`
	Action        string          `json:"action,omitempty"`
	Tool          string          `json:"tool"`
	Input         json.RawMessage `json:"input,omitempty"`
	ResultSummary string          `json:"resultSummary,omitempty"`
	Cost          *cost.Plan      `json:"cost,omitempty"`
	Allowed       bool            `json:"allowed"`
	// Code is the stable machine denial code (core/tool Denial.Code) when the
	// call was rejected by a governed path. Empty with Allowed=false means an
	// internal or protocol-level failure that is not reflected to clients.
	Code         string        `json:"code,omitempty"`
	Error        string        `json:"error,omitempty"`
	Duration     time.Duration `json:"-"`
	ReturnedRows int64         `json:"returnedRows"`
	// The SQL escape hatch (execute_sql) records two events per call, Phase
	// "begin" before the statement runs and "end" after, both carrying the
	// datasource, its connection and the statement as it is (never
	// redacted: the hatch has no entity to mask by); "end" adds the rows a
	// write affected. Other tools leave these unset.
	Phase        string `json:"phase,omitempty"`
	Datasource   string `json:"datasource,omitempty"`
	Connection   string `json:"connection,omitempty"`
	SQL          string `json:"sql,omitempty"`
	RowsAffected int64  `json:"rowsAffected,omitempty"`
}

// MarshalJSON emits Duration as integer milliseconds ("durationMs") so the
// JSON Lines contract does not depend on Go's nanosecond Duration encoding.
func (e Event) MarshalJSON() ([]byte, error) {
	type alias Event
	return json.Marshal(struct {
		alias
		DurationMS int64 `json:"durationMs"`
	}{alias(e), e.Duration.Milliseconds()})
}

// Auditor records an Event. It must not block or fail the caller (invariant I12).
type Auditor interface {
	Record(ctx context.Context, e Event) error
}

// Durable records an event before returning, and fails when the event was
// not written: what the SQL escape hatch (execute_sql) needs, as it refuses
// to run SQL it could not record (fail closed). FileSink is one, and an
// AsyncAuditor is one when given its sink (Durably).
type Durable interface {
	RecordSync(ctx context.Context, e Event) error
}

// ErrNotDurable is RecordSync on an AsyncAuditor without a durable sink.
var ErrNotDurable = errors.New("audit: no durable sink")

// NoopAuditor discards everything.
type NoopAuditor struct{}

// Record is a no-op.
func (NoopAuditor) Record(_ context.Context, _ Event) error { return nil }

// Sink consumes one event. Errors are ignored (best-effort).
type Sink func(Event) error

// AsyncAuditor pushes events to a bounded queue drained by a background
// flusher. On overflow it drops and counts (atomic) instead of blocking.
type AsyncAuditor struct {
	queue     chan Event
	sink      Sink
	durable   Durable
	closeSink func() error
	done      chan struct{}
	dropped   atomic.Int64
	closed    atomic.Bool
}

// Durably makes a record synchronously through d (its sink, typically) on
// RecordSync; without it RecordSync fails with ErrNotDurable.
func (a *AsyncAuditor) Durably(d Durable) *AsyncAuditor {
	a.durable = d
	return a
}

// RecordSync implements Durable through the durable sink: the event is
// written, and synced, before it returns, past the queue (events queued
// earlier may reach the sink after it; readers order events by time).
func (a *AsyncAuditor) RecordSync(ctx context.Context, e Event) error {
	if a.closed.Load() {
		return ErrSinkClosed
	}
	if a.durable == nil {
		return ErrNotDurable
	}
	return a.durable.RecordSync(ctx, e)
}

// NewAsyncAuditorWithClose starts a flusher goroutine and closes the sink (when
// closeSink is non-nil) after draining. Call Close to drain and stop.
func NewAsyncAuditorWithClose(sink Sink, closeSink func() error, queueSize int) *AsyncAuditor {
	return NewAsyncAuditorOn(nil, sink, closeSink, queueSize)
}

// NewAsyncAuditorOn is NewAsyncAuditorWithClose with the flusher a background
// task of b. On a closed bus the auditor starts closed: it records nothing.
func NewAsyncAuditorOn(b *bus.Bus, sink Sink, closeSink func() error, queueSize int) *AsyncAuditor {
	if queueSize <= 0 {
		queueSize = 1024
	}
	a := &AsyncAuditor{
		queue:     make(chan Event, queueSize),
		sink:      sink,
		closeSink: closeSink,
		done:      make(chan struct{}),
	}
	if !b.Go("audit flush", func(context.Context) { a.flush() }) {
		a.closed.Store(true)
		if closeSink != nil {
			_ = closeSink()
		}
		close(a.done)
	}
	return a
}

func (a *AsyncAuditor) flush() {
	defer func() {
		if a.closeSink != nil {
			_ = a.closeSink()
		}
		close(a.done)
	}()
	for e := range a.queue {
		if a.sink != nil {
			_ = a.sink(e)
		}
	}
}

// Record enqueues an event non-blocking. On a full queue it drops and counts.
func (a *AsyncAuditor) Record(_ context.Context, e Event) error {
	if a.closed.Load() {
		return ErrSinkClosed
	}
	select {
	case a.queue <- e:
	default:
		a.dropped.Add(1)
	}
	return nil
}

// Dropped returns the count of events dropped due to a full queue.
func (a *AsyncAuditor) Dropped() int64 { return a.dropped.Load() }

// Close stops accepting events and waits for the flusher to drain the queue.
func (a *AsyncAuditor) Close() {
	if a.closed.CompareAndSwap(false, true) {
		close(a.queue)
		<-a.done
	}
}
