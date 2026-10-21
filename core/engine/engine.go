package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/bus"
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
	io, inflight  int
	limiter       *ratelimit.Adaptive
	rps           *ratelimit.TokenBucket
	breaker       *ratelimit.Breaker
	quota         *Quota
	lane          *bus.Lane
	recordFailure func(error) bool
}

// WithIOPool sets the IO concurrency (<= DB connection pool size).
func WithIOPool(n int) Option { return func(c *config) { c.io = n } }

// WithMaxInflight sets the bounded in-flight queue (backpressure).
func WithMaxInflight(n int) Option { return func(c *config) { c.inflight = n } }

// WithLimiter attaches an adaptive limiter for AIMD feedback.
func WithLimiter(l *ratelimit.Adaptive) Option { return func(c *config) { c.limiter = l } }

// WithRPSLimiter attaches a token-bucket request-rate limiter.
func WithRPSLimiter(l *ratelimit.TokenBucket) Option { return func(c *config) { c.rps = l } }

// WithBreaker attaches a circuit breaker.
func WithBreaker(b *ratelimit.Breaker) Option { return func(c *config) { c.breaker = b } }

// WithQuota bounds IO by a quota shared with other engines; it replaces the
// IO pool, limiter, rate limiter and breaker options.
func WithQuota(q *Quota) Option { return func(c *config) { c.quota = q } }

// WithLane admits submissions on lane, such as the Serve lane of the
// process's bus, instead of an in-flight bound of the engine's own; it
// replaces the max in-flight option.
func WithLane(l *bus.Lane) Option { return func(c *config) { c.lane = l } }

// WithFailureClassifier decides whether a non-nil execution error represents
// provider/system health and should affect AIMD and the circuit breaker.
func WithFailureClassifier(classify func(error) bool) Option {
	return func(c *config) { c.recordFailure = classify }
}

// Engine bounds concurrency and deduplicates concurrent identical requests.
type Engine struct {
	quota         *Quota
	lane          *bus.Lane
	sf            singleflight
	recordFailure func(error) bool
	stateMu       sync.Mutex
	closed        bool
	active        sync.WaitGroup
	executions    sync.WaitGroup
}

// New returns an Engine with the given options and sane defaults.
func New(opts ...Option) (*Engine, error) {
	cfg := config{io: 16, inflight: 256}
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	if (cfg.quota == nil && cfg.io <= 0) || cfg.inflight <= 0 {
		return nil, ErrInvalidConfig
	}
	quota := cfg.quota
	if quota == nil {
		quota = NewQuota(cfg.io)
		quota.Update(cfg.io, cfg.limiter, cfg.rps, cfg.breaker)
	}
	lane := cfg.lane
	if lane == nil {
		lane = bus.NewLane(bus.Serve, bus.Limits{MaxConcurrent: cfg.inflight})
	}
	return &Engine{
		quota:         quota,
		lane:          lane,
		recordFailure: cfg.recordFailure,
	}, nil
}

// Submit schedules fn on e under backpressure and returns fn's result value
// and error. It returns ErrOverloaded if the in-flight queue is full,
// ErrCircuitOpen if the breaker is open, ErrRateLimited if concurrency
// exceeds the adaptive limit. Concurrent calls with the same non-empty key
// share a single execution (singleflight) and all receive the leader's
// result. An empty key opts out of de-duplication (writes/unique ops).
// Panics in fn are recovered as errors.
func Submit[T any](ctx context.Context, e *Engine, key string, fn func(context.Context) (T, error)) (T, error) {
	return typed[T](e.submit(ctx, key, erased(fn)))
}

// Run runs fn on e under the rate limit, its panics recovered, but outside
// the lane, the breaker, the adaptive limit and the IO slots: for a call
// ending a transaction, which must reach its database however it is held
// back, and holds its connection already.
func Run[T any](ctx context.Context, e *Engine, fn func(context.Context) (T, error)) (T, error) {
	return typed[T](e.run(ctx, erased(fn)))
}

// erased is fn returning its value as any, for the engine's untyped core
// (singleflight shares one value among callers).
func erased[T any](fn func(context.Context) (T, error)) func(context.Context) (any, error) {
	return func(ctx context.Context) (any, error) { return fn(ctx) }
}

// typed is v as a T; the zero T when v is not one (a failed call returns
// nil).
func typed[T any](v any, err error) (T, error) {
	t, _ := v.(T)
	return t, err
}

func (e *Engine) submit(ctx context.Context, key string, fn func(context.Context) (any, error)) (any, error) {
	done, err := e.admit()
	if err != nil {
		return nil, err
	}
	defer done()
	leave, err := e.lane.TryEnter()
	if err != nil {
		if errors.Is(err, bus.ErrOverloaded) {
			return nil, ErrOverloaded
		}
		return nil, ErrClosed
	}
	defer leave()
	if key == "" {
		return e.execute(ctx, fn)
	}
	return e.sf.Do(ctx, key, &e.executions, func(runCtx context.Context) (any, error) {
		return e.execute(runCtx, fn)
	})
}

func (e *Engine) run(ctx context.Context, fn func(context.Context) (any, error)) (any, error) {
	done, err := e.admit()
	if err != nil {
		return nil, err
	}
	defer done()
	return e.runSafe(ctx, fn)
}

// admit counts a call as active while the engine serves, under the rate
// limit; done ends it.
func (e *Engine) admit() (done func(), err error) {
	e.stateMu.Lock()
	if e.closed {
		e.stateMu.Unlock()
		return nil, ErrClosed
	}
	e.active.Add(1)
	e.stateMu.Unlock()
	if rps := e.quota.rps.Load(); rps != nil {
		if err := rps.Allow(); err != nil {
			e.active.Done()
			return nil, err
		}
	}
	return e.active.Done, nil
}

// execute runs fn under the breaker, the adaptive limit and an IO slot; the
// breaker is asked here, by the one execution, so every request it lets
// through ends with Record or Release.
func (e *Engine) execute(ctx context.Context, fn func(context.Context) (any, error)) (val any, err error) {
	limiter, breaker := e.quota.limiter.Load(), e.quota.breaker.Load()
	record := false
	if breaker != nil {
		probe, allowErr := breaker.Allow()
		if allowErr != nil {
			return nil, allowErr
		}
		defer func() {
			if record {
				breaker.Record(probe, err == nil)
			} else {
				breaker.Release(probe)
			}
		}()
	}
	if limiter != nil {
		if err := limiter.Acquire(); err != nil {
			return nil, err
		}
		defer limiter.Release()
	}
	if err := e.quota.io.Acquire(ctx); err != nil {
		return nil, err
	}
	defer e.quota.io.Release()
	start := time.Now()
	val, err = e.runSafe(ctx, fn)
	record = err == nil || e.recordFailure == nil || e.recordFailure(err)
	if record && limiter != nil {
		limiter.OnResult(err, time.Since(start))
	}
	return val, err
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
func (s *singleflight) Do(
	ctx context.Context,
	key string,
	executions *sync.WaitGroup,
	fn func(context.Context) (any, error),
) (any, error) {
	s.mu.Lock()
	if s.m == nil {
		s.m = make(map[string]*call)
	}
	if c, ok := s.m[key]; ok {
		c.waiters++
		s.mu.Unlock()
		return s.wait(ctx, key, c)
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
		defer func() {
			// A panic past the tool's own recovery (classifying its error,
			// the limits) ends the call, not the process.
			if r := recover(); r != nil {
				c.val, c.err = nil, fmt.Errorf("engine: panic recovered: %v", r)
			}
			cancel()
			s.mu.Lock()
			if s.m[key] == c {
				delete(s.m, key)
			}
			s.mu.Unlock()
			close(c.done)
		}()
		c.val, c.err = fn(runCtx)
	}()
	return s.wait(ctx, key, c)
}

// wait waits for c, the call of key. The last waiter to leave cancels it,
// and it leaves the map at once: a caller coming later starts a call of its
// own instead of waiting for the one canceled.
func (s *singleflight) wait(ctx context.Context, key string, c *call) (any, error) {
	select {
	case <-c.done:
		return c.val, c.err
	case <-ctx.Done():
		s.mu.Lock()
		c.waiters--
		if c.waiters == 0 {
			if s.m[key] == c {
				delete(s.m, key)
			}
			c.cancel()
		}
		s.mu.Unlock()
		return nil, ctx.Err()
	}
}
