package audit

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/cost"
)

// ErrSinkClosed is returned by Record on a closed AsyncAuditor.
var ErrSinkClosed = errors.New("audit: sink closed")

// Event records one tool invocation for compliance. DecisionID correlates the
// event with the MCP response and trace span of the same call.
type Event struct {
	Time          time.Time
	DecisionID    string
	Role          string
	Entity        string
	Action        string
	Tool          string
	Input         json.RawMessage
	ResultSummary string
	Cost          *cost.Plan
	Allowed       bool
	Error         string
	Duration      time.Duration
	ReturnedRows  int64
}

// Auditor records an Event. It must not block or fail the caller (invariant I12).
type Auditor interface {
	Record(ctx context.Context, e Event) error
}

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
	closeSink func() error
	done      chan struct{}
	dropped   atomic.Int64
	closed    atomic.Bool
}

// NewAsyncAuditor starts a flusher goroutine. Call Close to drain and stop.
func NewAsyncAuditor(sink Sink, queueSize int) *AsyncAuditor {
	return NewAsyncAuditorWithClose(sink, nil, queueSize)
}

// NewAsyncAuditorWithClose starts an auditor and closes the sink after draining.
func NewAsyncAuditorWithClose(sink Sink, closeSink func() error, queueSize int) *AsyncAuditor {
	if queueSize <= 0 {
		queueSize = 1024
	}
	a := &AsyncAuditor{
		queue:     make(chan Event, queueSize),
		sink:      sink,
		closeSink: closeSink,
		done:      make(chan struct{}),
	}
	go a.flush()
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
