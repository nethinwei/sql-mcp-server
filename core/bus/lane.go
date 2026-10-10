package bus

import (
	"context"
	"errors"
	"sync/atomic"
)

// Sentinel errors.
var (
	// ErrOverloaded is returned when a lane's concurrency and queue are full.
	ErrOverloaded = errors.New("bus: overloaded; retry later")
	// ErrClosed is returned once the bus is closed.
	ErrClosed = errors.New("bus: closed")
)

// LaneName names a lane.
type LaneName string

// The lanes of a bus.
const (
	// Serve runs tool calls; the engine admits them and takes their IO slots.
	Serve LaneName = "serve"
	// Admin runs reads that answer an administrator's request.
	Admin LaneName = "admin"
	// Scan runs schema scans.
	Scan LaneName = "scan"
	// Background runs work nobody waits for, such as capability assessment.
	Background LaneName = "background"
)

// Lanes lists the lanes in the order Stats reports them.
var Lanes = []LaneName{Serve, Admin, Scan, Background}

// Limits bound a lane: MaxConcurrent works at once and MaxQueue more waiting.
type Limits struct {
	MaxConcurrent int
	MaxQueue      int
}

// Lane bounds the concurrency of one kind of work. A nil *Lane runs work
// without bounds.
type Lane struct {
	name      LaneName
	bus       *Bus // nil for a lane of its own
	slots     *Semaphore
	maxQueue  atomic.Int64
	queued    atomic.Int64 // waiting for a lane slot, or holding a ticket
	ioWait    atomic.Int64 // holding a lane slot, waiting for IO
	inflight  atomic.Int64
	rejected  atomic.Int64
	completed atomic.Int64
}

// NewLane returns a lane of its own, on no bus: it takes no IO slots.
func NewLane(name LaneName, limits Limits) *Lane {
	return newLane(name, nil, limits)
}

func newLane(name LaneName, b *Bus, limits Limits) *Lane {
	l := &Lane{name: name, bus: b, slots: NewSemaphore(0)}
	l.update(limits)
	return l
}

func (l *Lane) update(limits Limits) {
	l.slots.Resize(max(1, limits.MaxConcurrent))
	l.maxQueue.Store(int64(max(0, limits.MaxQueue)))
}

// Name returns the lane's name.
func (l *Lane) Name() LaneName { return l.name }

// TryEnter admits work without waiting; release ends it. It takes no IO
// slot: the Serve lane's engine takes those itself.
func (l *Lane) TryEnter() (release func(), err error) {
	if l == nil {
		return func() {}, nil
	}
	if l.bus.isClosed() {
		return nil, ErrClosed
	}
	if !l.slots.TryAcquire() {
		l.rejected.Add(1)
		return nil, ErrOverloaded
	}
	l.inflight.Add(1)
	return l.leave, nil
}

func (l *Lane) leave() {
	l.inflight.Add(-1)
	l.completed.Add(1)
	l.slots.Release()
}

// Acquire admits work, waiting in the lane's queue until ctx ends, and takes
// an IO slot for it; the work runs on the returned context, which carries the
// admission, and release ends it. A full queue fails at once.
func (l *Lane) Acquire(ctx context.Context) (admitted context.Context, release func(), err error) {
	return l.acquire(ctx, true)
}

// acquire is Acquire; with bounded unset a full queue does not fail it.
func (l *Lane) acquire(ctx context.Context, bounded bool) (context.Context, func(), error) {
	if l == nil {
		return ctx, func() {}, nil
	}
	if l.bus.isClosed() {
		return nil, nil, ErrClosed
	}
	if !l.slots.TryAcquire() {
		if q := l.queued.Add(1); bounded && q > l.maxQueue.Load() {
			l.queued.Add(-1)
			l.rejected.Add(1)
			return nil, nil, ErrOverloaded
		}
		err := l.slots.Acquire(ctx)
		if err != nil {
			l.queued.Add(-1)
			return nil, nil, err
		}
		l.ioWait.Add(1) // before leaving the queue, so the work always counts
		l.queued.Add(-1)
	} else {
		l.ioWait.Add(1)
	}
	return l.admit(ctx)
}

// admit takes an IO slot for work holding a lane slot and counted as waiting
// for IO, which it then counts as running.
func (l *Lane) admit(ctx context.Context) (context.Context, func(), error) {
	releaseIO, err := l.bus.io(ctx, l.name != Serve)
	if err != nil {
		l.ioWait.Add(-1)
		l.slots.Release()
		return nil, nil, err
	}
	l.inflight.Add(1) // before no longer waiting, so the work always counts
	l.ioWait.Add(-1)
	return context.WithValue(ctx, admittedKey{}, l.name), func() {
		releaseIO()
		l.leave()
	}, nil
}

// Run runs fn once admitted; fn's context says it was, so Ensure runs nested
// work under this admission.
func (l *Lane) Run(ctx context.Context, fn func(context.Context) error) error {
	return l.run(ctx, true, fn)
}

// RunWaiting is Run that waits however full the queue is, for work that must
// not fail as busy and whose number the caller bounds, such as assembling a
// configuration (one at a time, a task per datasource).
func (l *Lane) RunWaiting(ctx context.Context, fn func(context.Context) error) error {
	return l.run(ctx, false, fn)
}

func (l *Lane) run(ctx context.Context, bounded bool, fn func(context.Context) error) error {
	if l == nil {
		return fn(ctx)
	}
	ctx, release, err := l.acquire(ctx, bounded)
	if err != nil {
		return err
	}
	defer release()
	return fn(ctx)
}

// Ticket is a place in a lane's queue, taken by Queue.
type Ticket struct {
	l    *Lane
	left atomic.Bool
}

// Queue takes a place in the lane's queue at once, for work admitted later
// in the order its caller chooses: it fails when the lane's running and
// waiting work already fill its concurrency and queue. The work counts as
// waiting until the ticket's Acquire admits it or Cancel gives the place up.
func (l *Lane) Queue() (*Ticket, error) {
	if l == nil {
		return &Ticket{}, nil
	}
	if l.bus.isClosed() {
		return nil, ErrClosed
	}
	capacity := int64(l.slots.Stats().Size) + l.maxQueue.Load()
	if q := l.queued.Add(1); q+l.ioWait.Load()+l.inflight.Load() > capacity {
		l.queued.Add(-1)
		l.rejected.Add(1)
		return nil, ErrOverloaded
	}
	return &Ticket{l: l}, nil
}

// Acquire admits the ticket's work, waiting until ctx ends; see Lane.Acquire.
// It fails with context.Canceled once the ticket was canceled.
func (t *Ticket) Acquire(ctx context.Context) (admitted context.Context, release func(), err error) {
	if t.l == nil {
		return ctx, func() {}, nil
	}
	if t.left.Load() {
		return nil, nil, context.Canceled
	}
	if err = t.l.slots.Acquire(ctx); err != nil {
		t.leave()
		return nil, nil, err
	}
	t.l.ioWait.Add(1) // before leaving the queue, so the work always counts
	if !t.leave() {   // canceled while waiting
		t.l.ioWait.Add(-1)
		t.l.slots.Release()
		return nil, nil, context.Canceled
	}
	return t.l.admit(ctx)
}

// Cancel gives up the ticket's place; its Acquire then fails.
func (t *Ticket) Cancel() { t.leave() }

// leave takes the ticket out of the queue; false when it already left.
func (t *Ticket) leave() bool {
	if t.l == nil || !t.left.CompareAndSwap(false, true) {
		return false
	}
	t.l.queued.Add(-1)
	return true
}

// Ensure runs fn under the admission ctx carries, or, without one, as Run.
func (l *Lane) Ensure(ctx context.Context, fn func(context.Context) error) error {
	if _, ok := Admitted(ctx); ok {
		return fn(ctx)
	}
	return l.Run(ctx, fn)
}

type admittedKey struct{}

// Admitted returns the lane whose admission ctx carries.
func Admitted(ctx context.Context) (LaneName, bool) {
	name, ok := ctx.Value(admittedKey{}).(LaneName)
	return name, ok
}

// Stats returns the lane's limits and counts.
func (l *Lane) Stats() LaneStats {
	slots := l.slots.Stats()
	return LaneStats{
		Name: l.name, MaxConcurrent: slots.Size, MaxQueue: int(l.maxQueue.Load()),
		Inflight: int(l.inflight.Load()), Queued: int(l.queued.Load() + l.ioWait.Load()),
		Rejected: l.rejected.Load(), Completed: l.completed.Load(),
	}
}
