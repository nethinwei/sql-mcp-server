package bus

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultLimits are the lane limits of a bus given none.
var DefaultLimits = map[LaneName]Limits{
	Serve:      {MaxConcurrent: 256},
	Admin:      {MaxConcurrent: 4, MaxQueue: 16},
	Scan:       {MaxConcurrent: 2, MaxQueue: 8},
	Background: {MaxConcurrent: 2, MaxQueue: 64},
}

// DefaultIO is the IO slots of a bus given none.
const DefaultIO = 16

// Bus admits the process's database work and runs its background tasks. A
// nil *Bus has nil lanes, which run work without bounds, and starts tasks
// that still recover from panics but are neither counted nor waited for.
type Bus struct {
	ioSlots *Semaphore
	aux     *Semaphore // the IO the lanes other than Serve may hold together
	lanes   map[LaneName]*Lane

	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	// closed is set under mu, so that no task enters once it is; every
	// call reads it, without the lock.
	closed atomic.Bool
	tasks  map[string]int
	wg     sync.WaitGroup
}

// New returns a bus of io IO slots (DefaultIO when not positive) and lanes
// limited as limits says, DefaultLimits for the ones it leaves out.
func New(io int, limits map[LaneName]Limits) *Bus {
	ctx, cancel := context.WithCancel(context.Background())
	b := &Bus{
		ioSlots: NewSemaphore(0), aux: NewSemaphore(0), lanes: map[LaneName]*Lane{},
		ctx: ctx, cancel: cancel, tasks: map[string]int{},
	}
	for _, name := range Lanes {
		b.lanes[name] = newLane(name, b, laneLimits(name, limits))
	}
	b.resizeIO(io)
	return b
}

func laneLimits(name LaneName, limits map[LaneName]Limits) Limits {
	if l, ok := limits[name]; ok && l.MaxConcurrent > 0 {
		return l
	}
	return DefaultLimits[name]
}

func (b *Bus) resizeIO(io int) {
	if io <= 0 {
		io = DefaultIO
	}
	b.ioSlots.Resize(io)
	b.aux.Resize(max(1, io/2))
}

// Update sets the IO slots and the lane limits; work holding a slot keeps it.
func (b *Bus) Update(io int, limits map[LaneName]Limits) {
	if b == nil {
		return
	}
	b.resizeIO(io)
	for name, lane := range b.lanes {
		lane.update(laneLimits(name, limits))
	}
}

// Lane returns the lane named name; nil on a nil bus.
func (b *Bus) Lane(name LaneName) *Lane {
	if b == nil {
		return nil
	}
	return b.lanes[name]
}

// IO returns the IO semaphore every lane draws on; nil on a nil bus.
func (b *Bus) IO() *Semaphore {
	if b == nil {
		return nil
	}
	return b.ioSlots
}

// io takes an IO slot, after an auxiliary one when aux is set.
func (b *Bus) io(ctx context.Context, aux bool) (func(), error) {
	if b == nil {
		return func() {}, nil
	}
	if aux {
		if err := b.aux.Acquire(ctx); err != nil {
			return nil, err
		}
	}
	if err := b.ioSlots.Acquire(ctx); err != nil {
		if aux {
			b.aux.Release()
		}
		return nil, err
	}
	return func() {
		b.ioSlots.Release()
		if aux {
			b.aux.Release()
		}
	}, nil
}

func (b *Bus) isClosed() bool {
	if b == nil {
		return false
	}
	return b.closed.Load()
}

// Go runs fn as the background task name; fn's context ends on Close. It
// returns false, not running fn, once the bus is closed.
func (b *Bus) Go(name string, fn func(context.Context)) bool {
	if b == nil {
		go runTask(name, func() { fn(context.Background()) })
		return true
	}
	if !b.enter(name) {
		return false
	}
	go func() {
		defer b.exit(name)
		runTask(name, func() { fn(b.ctx) })
	}()
	return true
}

// Fork runs fn as the task name and marks it done on wg, for work a caller
// waits for on wg. Once the bus is closed fn still runs, uncounted, so the
// caller never waits for work that does not come.
func (b *Bus) Fork(wg *sync.WaitGroup, name string, fn func(context.Context)) {
	wg.Add(1)
	run := func(ctx context.Context) {
		defer wg.Done()
		fn(ctx)
	}
	if !b.Go(name, run) {
		go runTask(name, func() { run(context.Background()) })
	}
}

// AfterFunc runs fn as the background task name once d has passed, unless
// stop is called first or the bus is closed by then.
func (b *Bus) AfterFunc(name string, d time.Duration, fn func()) (stop func() bool) {
	t := time.AfterFunc(d, func() {
		if b == nil {
			runTask(name, fn)
			return
		}
		if !b.enter(name) {
			return
		}
		defer b.exit(name)
		runTask(name, fn)
	})
	return t.Stop
}

func (b *Bus) enter(name string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed.Load() {
		return false
	}
	b.tasks[name]++
	b.wg.Add(1)
	return true
}

func (b *Bus) exit(name string) {
	b.mu.Lock()
	if b.tasks[name]--; b.tasks[name] == 0 {
		delete(b.tasks, name)
	}
	b.mu.Unlock()
	b.wg.Done()
}

// runTask runs a task, logging a panic instead of stopping the process.
func runTask(name string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("background task panicked", "task", name, "panic", fmt.Sprint(r),
				"stack", string(debug.Stack()))
		}
	}()
	fn()
}

// Close rejects new work and tasks, cancels the tasks' context and waits for
// them until ctx ends.
func (b *Bus) Close(ctx context.Context) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	b.closed.Store(true)
	b.mu.Unlock()
	b.cancel()
	done := make(chan struct{})
	go func() {
		b.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stats is a snapshot of a bus.
type Stats struct {
	IO    SlotStats
	Aux   SlotStats
	Lanes []LaneStats
	Tasks []TaskStats
}

// SlotStats describe a semaphore.
type SlotStats struct {
	Size, Used, Waiting int
}

// LaneStats describe a lane.
type LaneStats struct {
	Name                    LaneName
	MaxConcurrent, MaxQueue int
	Inflight, Queued        int
	Rejected, Completed     int64
}

// TaskStats count the running background tasks of one name.
type TaskStats struct {
	Name    string
	Running int
}

// Stats returns a snapshot of the bus; empty on a nil bus.
func (b *Bus) Stats() Stats {
	if b == nil {
		return Stats{}
	}
	st := Stats{IO: b.ioSlots.Stats(), Aux: b.aux.Stats()}
	for _, name := range Lanes {
		st.Lanes = append(st.Lanes, b.lanes[name].Stats())
	}
	b.mu.Lock()
	for name, n := range b.tasks {
		st.Tasks = append(st.Tasks, TaskStats{Name: name, Running: n})
	}
	b.mu.Unlock()
	slices.SortFunc(st.Tasks, func(x, y TaskStats) int { return cmp.Compare(x.Name, y.Name) })
	return st
}
