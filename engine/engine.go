package engine

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nethinwei/sql-mcp-server/ratelimit"
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
	breaker           *ratelimit.Breaker
}

// WithIOPool sets the IO concurrency (<= DB connection pool size).
func WithIOPool(n int) Option { return func(c *config) { c.io = n } }

// WithCPUPool sets the CPU concurrency (<= GOMAXPROCS).
func WithCPUPool(n int) Option { return func(c *config) { c.cpu = n } }

// WithMaxInflight sets the bounded in-flight queue (backpressure).
func WithMaxInflight(n int) Option { return func(c *config) { c.inflight = n } }

// WithLimiter attaches an adaptive limiter for AIMD feedback.
func WithLimiter(l *ratelimit.Adaptive) Option { return func(c *config) { c.limiter = l } }

// WithBreaker attaches a circuit breaker.
func WithBreaker(b *ratelimit.Breaker) Option { return func(c *config) { c.breaker = b } }

// Engine bounds concurrency and deduplicates concurrent identical requests.
type Engine struct {
	iosem    chan struct{}
	inflight chan struct{}
	sf       singleflight
	limiter  *ratelimit.Adaptive
	breaker  *ratelimit.Breaker
	closed   atomic.Bool
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
		iosem:    make(chan struct{}, cfg.io),
		inflight: make(chan struct{}, cfg.inflight),
		limiter:  cfg.limiter,
		breaker:  cfg.breaker,
	}, nil
}

// Submit schedules fn under backpressure and returns fn's result value and
// error. It returns ErrOverloaded if the in-flight queue is full, ErrCircuitOpen
// if the breaker is open, ErrRateLimited if concurrency exceeds the adaptive
// limit. Concurrent calls with the same non-empty key share a single execution
// (singleflight) and all receive the leader's result. An empty key opts out of
// de-duplication (writes/unique ops). Panics in fn are recovered as errors.
func (e *Engine) Submit(ctx context.Context, key string, fn func(context.Context) (any, error)) (any, error) {
	if e.closed.Load() {
		return nil, ErrClosed
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
	exec := func() (any, error) {
		// AIMD admission: reject fast when concurrency exceeds the adaptive
		// limit, then bound IO concurrency by the pool semaphore.
		if e.limiter != nil {
			if err := e.limiter.Acquire(); err != nil {
				return nil, err
			}
			defer e.limiter.Release()
		}
		select {
		case e.iosem <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		defer func() { <-e.iosem }()
		start := time.Now()
		val, err := e.runSafe(ctx, fn)
		if e.limiter != nil {
			e.limiter.OnResult(err, time.Since(start))
		}
		if e.breaker != nil {
			e.breaker.Record(err == nil)
		}
		return val, err
	}
	if key == "" {
		return exec()
	}
	return e.sf.Do(key, exec)
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

// Close marks the engine closed; subsequent Submit returns ErrClosed.
func (e *Engine) Close() { e.closed.Store(true) }

// singleflight deduplicates concurrent calls by key using only the standard
// library. Waiters receive the leader's result value and error.
type singleflight struct {
	mu sync.Mutex
	m  map[string]*call
}

type call struct {
	wg  sync.WaitGroup
	val any
	err error
}

// Do executes fn once for concurrent callers with the same key; all callers
// receive the same result value and error. wg.Done and map cleanup are deferred
// so a panic in fn cannot deadlock waiters.
func (s *singleflight) Do(key string, fn func() (any, error)) (any, error) {
	s.mu.Lock()
	if s.m == nil {
		s.m = make(map[string]*call)
	}
	if c, ok := s.m[key]; ok {
		s.mu.Unlock()
		c.wg.Wait()
		return c.val, c.err
	}
	c := &call{}
	c.wg.Add(1)
	s.m[key] = c
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.m, key)
		s.mu.Unlock()
	}()
	defer c.wg.Done()
	c.val, c.err = fn()
	return c.val, c.err
}
