package engine

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/ratelimit"
)

// Sentinel errors.
var (
	// ErrOverloaded is returned when the in-flight queue is full (backpressure).
	ErrOverloaded = errors.New("engine: overloaded")
	// ErrInvalidConfig is returned by New for non-positive pool sizes.
	ErrInvalidConfig = errors.New("engine: invalid config")
	// ErrClosed is returned by Submit after Close.
	ErrClosed = errors.New("engine: closed")
)

// Option configures an Engine.
type Option func(*config)

type config struct {
	io, cpu, inflight int
	limiter           *ratelimit.Adaptive
	rps               *ratelimit.TokenBucket
	breaker           *ratelimit.Breaker
	recordFailure     func(error) bool
}

// WithIOPool sets the IO concurrency (<= DB connection pool size).
func WithIOPool(n int) Option { return func(c *config) { c.io = n } }

// WithCPUPool sets the CPU concurrency (<= GOMAXPROCS).
func WithCPUPool(n int) Option { return func(c *config) { c.cpu = n } }

// WithMaxInflight sets the bounded in-flight queue (backpressure).
func WithMaxInflight(n int) Option { return func(c *config) { c.inflight = n } }

// WithLimiter attaches an adaptive limiter for AIMD feedback.
func WithLimiter(l *ratelimit.Adaptive) Option { return func(c *config) { c.limiter = l } }

// WithRPSLimiter attaches a token-bucket request-rate limiter.
func WithRPSLimiter(l *ratelimit.TokenBucket) Option { return func(c *config) { c.rps = l } }

// WithBreaker attaches a circuit breaker.
func WithBreaker(b *ratelimit.Breaker) Option { return func(c *config) { c.breaker = b } }

// WithFailureClassifier decides whether a non-nil execution error represents
// provider/system health and should affect AIMD and the circuit breaker.
func WithFailureClassifier(classify func(error) bool) Option {
	return func(c *config) { c.recordFailure = classify }
}

// Engine bounds concurrency and deduplicates concurrent identical requests.
type Engine struct {
	iosem         chan struct{}
	cpusem        chan struct{}
	inflight      chan struct{}
	sf            singleflight
	limiter       *ratelimit.Adaptive
	rps           *ratelimit.TokenBucket
	breaker       *ratelimit.Breaker
	recordFailure func(error) bool
	stateMu       sync.Mutex
	closed        bool
	active        sync.WaitGroup
	executions    sync.WaitGroup
}

// New returns an Engine with the given options and sane defaults.
func New(opts ...Option) (*Engine, error) {
	cfg := config{io: 16, cpu: runtime.NumCPU(), inflight: 256}
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	if cfg.io <= 0 || cfg.cpu <= 0 || cfg.inflight <= 0 {
		return nil, ErrInvalidConfig
	}
	return &Engine{
		iosem:         make(chan struct{}, cfg.io),
		cpusem:        make(chan struct{}, cfg.cpu),
		inflight:      make(chan struct{}, cfg.inflight),
		limiter:       cfg.limiter,
		rps:           cfg.rps,
		breaker:       cfg.breaker,
		recordFailure: cfg.recordFailure,
	}, nil
}

// WorkClass selects the resource pool used by a submission.
type WorkClass uint8

const (
	// WorkIO is database or network work and uses the IO pool.
	WorkIO WorkClass = iota
	// WorkCPU is compute-heavy work and uses the CPU pool.
	WorkCPU
)

// Submit schedules fn under backpressure and returns fn's result value and
// error. It returns ErrOverloaded if the in-flight queue is full, ErrCircuitOpen
// if the breaker is open, ErrRateLimited if concurrency exceeds the adaptive
// limit. Concurrent calls with the same non-empty key share a single execution
// (singleflight) and all receive the leader's result. An empty key opts out of
// de-duplication (writes/unique ops). Panics in fn are recovered as errors.
func (e *Engine) Submit(ctx context.Context, key string, fn func(context.Context) (any, error)) (any, error) {
	return e.SubmitClass(ctx, WorkIO, key, fn)
}

// SubmitCPU schedules compute-heavy work on the CPU pool.
func (e *Engine) SubmitCPU(ctx context.Context, key string, fn func(context.Context) (any, error)) (any, error) {
	return e.SubmitClass(ctx, WorkCPU, key, fn)
}

// SubmitClass schedules fn on the selected resource pool. Submit remains the
// backwards-compatible IO entry point.
func (e *Engine) SubmitClass(ctx context.Context, class WorkClass, key string, fn func(context.Context) (any, error)) (any, error) {
	e.stateMu.Lock()
	if e.closed {
		e.stateMu.Unlock()
		return nil, ErrClosed
	}
	e.active.Add(1)
	e.stateMu.Unlock()
	defer e.active.Done()
	if e.rps != nil {
		if err := e.rps.Allow(); err != nil {
			return nil, err
		}
	}
	if e.breaker != nil {
		if err := e.breaker.Allow(); err != nil {
			return nil, err
		}
	}
	select {
	case e.inflight <- struct{}{}:
	default:
		return nil, ErrOverloaded
	}
	defer func() { <-e.inflight }()
	exec := func(runCtx context.Context) (any, error) {
		// AIMD admission: reject fast when concurrency exceeds the adaptive
		// limit for IO work, then use the selected resource semaphore.
		if class == WorkIO && e.limiter != nil {
			if err := e.limiter.Acquire(); err != nil {
				return nil, err
			}
			defer e.limiter.Release()
		}
		sem := e.iosem
		if class == WorkCPU {
			sem = e.cpusem
		}
		select {
		case sem <- struct{}{}:
		case <-runCtx.Done():
			return nil, runCtx.Err()
		}
		defer func() { <-sem }()
		start := time.Now()
		val, err := e.runSafe(runCtx, fn)
		record := err == nil || e.recordFailure == nil || e.recordFailure(err)
		if record {
			if e.limiter != nil {
				e.limiter.OnResult(err, time.Since(start))
			}
			if e.breaker != nil {
				e.breaker.Record(err == nil)
			}
		}
		return val, err
	}
	if key == "" {
		return exec(ctx)
	}
	return e.sf.Do(ctx, fmt.Sprintf("%d\x00%s", class, key), &e.executions, exec)
}

// runSafe recovers panics from fn so a single bad call cannot crash the process.
func (e *Engine) runSafe(ctx context.Context, fn func(context.Context) (any, error)) (val any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("engine: panic recovered: %v", r)
		}
	}()
	return fn(ctx)
}

// Drain rejects new submissions and waits for admitted callers and detached
// singleflight execution goroutines to return.
func (e *Engine) Drain(ctx context.Context) error {
	e.stateMu.Lock()
	e.closed = true
	e.stateMu.Unlock()
	done := make(chan struct{})
	go func() {
		e.active.Wait()
		e.executions.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close rejects new work and waits for all in-flight submissions.
func (e *Engine) Close() { _ = e.Drain(context.Background()) }

// singleflight deduplicates concurrent calls by key using only the standard
// library. Waiters receive the leader's result value and error.
type singleflight struct {
	mu sync.Mutex
	m  map[string]*call
}

type call struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	val     any
	err     error
}

// Do executes fn once for concurrent callers with the same key; all callers
// receive the same result value and error. Completion signaling and map cleanup
// are deferred so a panic in fn cannot deadlock waiters.
func (s *singleflight) Do(ctx context.Context, key string, executions *sync.WaitGroup, fn func(context.Context) (any, error)) (any, error) {
	s.mu.Lock()
	if s.m == nil {
		s.m = make(map[string]*call)
	}
	if c, ok := s.m[key]; ok {
		c.waiters++
		s.mu.Unlock()
		return s.wait(ctx, c)
	}
	baseCtx := context.WithoutCancel(ctx)
	var runCtx context.Context
	var cancel context.CancelFunc
	if deadline, ok := ctx.Deadline(); ok {
		runCtx, cancel = context.WithDeadline(baseCtx, deadline)
	} else {
		runCtx, cancel = context.WithCancel(baseCtx)
	}
	c := &call{done: make(chan struct{}), cancel: cancel, waiters: 1}
	s.m[key] = c
	executions.Add(1)
	s.mu.Unlock()
	go func() {
		defer executions.Done()
		c.val, c.err = fn(runCtx)
		cancel()
		s.mu.Lock()
		if s.m[key] == c {
			delete(s.m, key)
		}
		s.mu.Unlock()
		close(c.done)
	}()
	return s.wait(ctx, c)
}

func (s *singleflight) wait(ctx context.Context, c *call) (any, error) {
	select {
	case <-c.done:
		return c.val, c.err
	case <-ctx.Done():
		s.mu.Lock()
		c.waiters--
		if c.waiters == 0 {
			c.cancel()
		}
		s.mu.Unlock()
		return nil, ctx.Err()
	}
}
