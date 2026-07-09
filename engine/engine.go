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

// Submit schedules fn under backpressure. It returns ErrOverloaded if the
// in-flight queue is full, ErrCircuitOpen if the breaker is open. Concurrent
// calls with the same key share a single execution (singleflight). Panics in fn
// are recovered and returned as errors.
func (e *Engine) Submit(ctx context.Context, key string, fn func(context.Context) error) error {
	if e.closed.Load() {
		return ErrClosed
	}
	if e.breaker != nil {
		if err := e.breaker.Allow(); err != nil {
			return err
		}
	}
	select {
	case e.inflight <- struct{}{}:
	default:
		return ErrOverloaded
	}
	defer func() { <-e.inflight }()
	return e.sf.Do(key, func() error {
		select {
		case e.iosem <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		defer func() { <-e.iosem }()
		start := time.Now()
		err := e.runSafe(ctx, fn)
		if e.limiter != nil {
			e.limiter.OnResult(err, time.Since(start))
		}
		if e.breaker != nil {
			e.breaker.Record(err == nil)
		}
		return err
	})
}

// runSafe recovers panics from fn so a single bad call cannot crash the process.
func (e *Engine) runSafe(ctx context.Context, fn func(context.Context) error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("engine: panic recovered: %v", r)
		}
	}()
	return fn(ctx)
}

// Close marks the engine closed; subsequent Submit returns ErrClosed.
func (e *Engine) Close() { e.closed.Store(true) }

// singleflight deduplicates concurrent calls by key using only the standard library.
type singleflight struct {
	mu sync.Mutex
	m  map[string]*call
}

type call struct {
	wg  sync.WaitGroup
	err error
}

// Do executes fn once for concurrent callers with the same key.
func (s *singleflight) Do(key string, fn func() error) error {
	s.mu.Lock()
	if s.m == nil {
		s.m = make(map[string]*call)
	}
	if c, ok := s.m[key]; ok {
		s.mu.Unlock()
		c.wg.Wait()
		return c.err
	}
	c := &call{}
	c.wg.Add(1)
	s.m[key] = c
	s.mu.Unlock()
	c.err = fn()
	c.wg.Done()
	s.mu.Lock()
	delete(s.m, key)
	s.mu.Unlock()
	return c.err
}
