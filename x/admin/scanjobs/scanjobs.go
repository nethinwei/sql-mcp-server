// Package scanjobs runs long administrative work, such as schema scans, on
// the Scan lane of the execution bus, off the request path: a request starts
// a job and polls it, so a slow database ties up neither the request nor the
// service.
package scanjobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/bus"
)

// ErrBusy is returned when the queue is full.
var ErrBusy = errors.New("scanjobs: too many jobs queued; retry later")

// State is where a job is in its life.
type State string

const (
	Pending  State = "PENDING"
	Running  State = "RUNNING"
	Done     State = "DONE"
	Failed   State = "FAILED"
	Canceled State = "CANCELED"
)

// Job is a snapshot of one job.
type Job[T any] struct {
	ID string
	// Label says what the job works on (a datasource name).
	Label  string
	State  State
	Result T
	Err    string
}

// Options size the pool.
type Options struct {
	// Bus runs the jobs on its Scan lane, whose limits then replace Workers
	// and Queue; without one the pool has a lane of its own.
	Bus     *bus.Bus
	Workers int           // concurrent jobs (default 2)
	Queue   int           // jobs waiting for a worker (default 8)
	Timeout time.Duration // per job (default 5m)
	// Keep is how long a finished job stays readable (default 10m).
	Keep time.Duration
}

type job[T any] struct {
	Job[T]
	run      func(context.Context) (T, error)
	ctx      context.Context
	cancel   context.CancelFunc
	ticket   *bus.Ticket // the job's place in the Scan lane's queue
	finished time.Time
}

// Pool runs jobs that produce a T.
type Pool[T any] struct {
	opts Options
	lane *bus.Lane
	mu   sync.Mutex
	jobs map[string]*job[T]
	// pending are the jobs waiting, in order; ready signals one was added.
	pending []*job[T]
	ready   chan struct{}
	stop    context.CancelFunc
	ctx     context.Context
	wg      sync.WaitGroup
}

// New returns a pool; Close stops it.
func New[T any](opts Options) *Pool[T] {
	opts = withDefaults(opts)
	ctx, stop := context.WithCancel(context.Background())
	lane := opts.Bus.Lane(bus.Scan)
	if lane == nil {
		lane = bus.NewLane(bus.Scan, bus.Limits{MaxConcurrent: opts.Workers, MaxQueue: opts.Queue})
	}
	p := &Pool[T]{opts: opts, lane: lane, jobs: map[string]*job[T]{}, ready: make(chan struct{}, 1), ctx: ctx,
		stop: stop}
	opts.Bus.Fork(&p.wg, "scan dispatcher", p.dispatch)
	return p
}

func withDefaults(o Options) Options {
	if o.Workers <= 0 {
		o.Workers = 2
	}
	if o.Queue <= 0 {
		o.Queue = 8
	}
	if o.Timeout <= 0 {
		o.Timeout = 5 * time.Minute
	}
	if o.Keep <= 0 {
		o.Keep = 10 * time.Minute
	}
	return o
}

// Start queues run, labeled label, and returns the pending job.
func (p *Pool[T]) Start(label string, run func(context.Context) (T, error)) (Job[T], error) {
	id, err := newID()
	if err != nil {
		return Job[T]{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.expire()
	if p.ctx.Err() != nil {
		return Job[T]{}, ErrBusy
	}
	// The job waits in the Scan lane's queue from now on, and the lane's
	// limits say whether there is room for it.
	ticket, err := p.lane.Queue()
	if err != nil {
		return Job[T]{}, ErrBusy
	}
	ctx, cancel := context.WithCancel(p.ctx)
	j := &job[T]{Job: Job[T]{ID: id, Label: label, State: Pending}, run: run, ctx: ctx, cancel: cancel, ticket: ticket}
	p.jobs[id] = j
	p.pending = append(p.pending, j)
	select {
	case p.ready <- struct{}{}:
	default:
	}
	return j.Job, nil
}

// Get returns the job id, if it exists and has not expired.
func (p *Pool[T]) Get(id string) (Job[T], bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.expire()
	j, ok := p.jobs[id]
	if !ok {
		return Job[T]{}, false
	}
	return j.Job, true
}

// Cancel stops job id: a pending job never runs, a running one has its
// context canceled.
func (p *Pool[T]) Cancel(id string) (Job[T], bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	j, ok := p.jobs[id]
	if !ok {
		return Job[T]{}, false
	}
	switch j.State {
	case Pending:
		p.finish(j, Canceled, "canceled")
		j.ticket.Cancel()
		j.cancel()
	case Running:
		j.cancel()
	}
	return j.Job, true
}

// Close cancels every job and waits for them.
func (p *Pool[T]) Close() {
	p.stop()
	p.wg.Wait()
}

// dispatch admits the pending jobs on the Scan lane, in order, and runs
// each once admitted, until the pool closes. Closing the bus (busCtx)
// closes the pool at once: the jobs waiting for admission or running end.
func (p *Pool[T]) dispatch(busCtx context.Context) {
	defer context.AfterFunc(busCtx, p.stop)()
	defer p.dropPending()
	for {
		j := p.next()
		if j == nil {
			return
		}
		ctx, release, err := j.ticket.Acquire(j.ctx)
		if err != nil {
			p.end(j, *new(T), err)
			continue
		}
		p.opts.Bus.Fork(&p.wg, "scan job", func(context.Context) {
			defer release()
			p.runJob(ctx, j)
		})
	}
}

// dropPending gives up the queue places of the jobs never dispatched.
func (p *Pool[T]) dropPending() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, j := range p.pending {
		j.ticket.Cancel()
	}
	p.pending = nil
}

// next waits for the first pending job; nil once the pool closes.
func (p *Pool[T]) next() *job[T] {
	for {
		p.mu.Lock()
		if len(p.pending) > 0 {
			j := p.pending[0]
			p.pending = p.pending[1:]
			p.mu.Unlock()
			return j
		}
		p.mu.Unlock()
		select {
		case <-p.ready:
		case <-p.ctx.Done():
			return nil
		}
	}
}

// runJob runs an admitted job unless it was canceled while pending.
func (p *Pool[T]) runJob(ctx context.Context, j *job[T]) {
	ctx, cancel := context.WithTimeout(ctx, p.opts.Timeout)
	defer cancel()
	p.mu.Lock()
	if j.State != Pending {
		p.mu.Unlock()
		p.end(j, *new(T), nil)
		return
	}
	j.State = Running
	p.mu.Unlock()
	result, err := runSafely(ctx, j)
	if errors.Is(ctx.Err(), context.Canceled) {
		err = context.Canceled
	}
	p.end(j, result, err)
}

// end records how j ended, unless it was canceled while pending.
func (p *Pool[T]) end(j *job[T], result T, err error) {
	j.cancel()
	p.mu.Lock()
	defer p.mu.Unlock()
	if j.State != Pending && j.State != Running {
		return
	}
	switch {
	case errors.Is(err, context.Canceled):
		p.finish(j, Canceled, "canceled")
	case err != nil:
		p.finish(j, Failed, err.Error())
	default:
		j.Result = result
		p.finish(j, Done, "")
	}
}

// runSafely runs a job and turns a panic (a driver bug, say) into its
// failure: a job runs on a pool goroutine, and a panic there would stop the
// whole process.
func runSafely[T any](ctx context.Context, j *job[T]) (result T, err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("scan job panicked", "job", j.Label, "panic", r, "stack", string(debug.Stack()))
			err = fmt.Errorf("internal error: %v", r)
		}
	}()
	return j.run(ctx)
}

// finish records the outcome; p.mu is held.
func (p *Pool[T]) finish(j *job[T], state State, msg string) {
	j.State, j.Err, j.finished = state, msg, time.Now()
}

// expire drops jobs finished longer than Keep ago; p.mu is held.
func (p *Pool[T]) expire() {
	for id, j := range p.jobs {
		if !j.finished.IsZero() && time.Since(j.finished) > p.opts.Keep {
			delete(p.jobs, id)
		}
	}
}

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
