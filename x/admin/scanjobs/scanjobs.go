// Package scanjobs runs long administrative work, such as schema scans, on a
// bounded pool of workers off the request path: a request starts a job and
// polls it, so a slow database ties up neither the request nor the service.
package scanjobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
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
	Workers int           // concurrent jobs (default 2)
	Queue   int           // jobs waiting for a worker (default 8)
	Timeout time.Duration // per job (default 5m)
	// Keep is how long a finished job stays readable (default 10m).
	Keep time.Duration
}

type job[T any] struct {
	Job[T]
	run      func(context.Context) (T, error)
	cancel   context.CancelFunc
	finished time.Time
}

// Pool runs jobs that produce a T.
type Pool[T any] struct {
	opts  Options
	queue chan *job[T]
	mu    sync.Mutex
	jobs  map[string]*job[T]
	stop  context.CancelFunc
	ctx   context.Context
	wg    sync.WaitGroup
}

// New starts a pool; Close stops it.
func New[T any](opts Options) *Pool[T] {
	opts = withDefaults(opts)
	ctx, stop := context.WithCancel(context.Background())
	p := &Pool[T]{opts: opts, queue: make(chan *job[T], opts.Queue), jobs: map[string]*job[T]{}, ctx: ctx, stop: stop}
	for range opts.Workers {
		p.wg.Go(p.work)
	}
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
	j := &job[T]{Job: Job[T]{ID: id, Label: label, State: Pending}, run: run}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.expire()
	select {
	case p.queue <- j:
	default:
		return Job[T]{}, ErrBusy
	}
	p.jobs[id] = j
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
	case Running:
		j.cancel()
	}
	return j.Job, true
}

// Close cancels every job and waits for the workers.
func (p *Pool[T]) Close() {
	p.stop()
	p.wg.Wait()
}

func (p *Pool[T]) work() {
	for {
		select {
		case <-p.ctx.Done():
			return
		case j := <-p.queue:
			p.runJob(j)
		}
	}
}

func (p *Pool[T]) runJob(j *job[T]) {
	ctx, cancel := context.WithTimeout(p.ctx, p.opts.Timeout)
	defer cancel()
	p.mu.Lock()
	if j.State != Pending {
		p.mu.Unlock()
		return
	}
	j.State, j.cancel = Running, cancel
	p.mu.Unlock()
	result, err := j.run(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		p.finish(j, Canceled, "canceled")
	case err != nil:
		p.finish(j, Failed, err.Error())
	default:
		j.Result = result
		p.finish(j, Done, "")
	}
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
