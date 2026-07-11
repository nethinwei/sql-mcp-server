package ratelimit

import (
	"errors"
	"sync/atomic"
	"time"
)

// ErrRateLimited is returned when concurrency exceeds the current adaptive limit.
var ErrRateLimited = errors.New("ratelimit: concurrency limit reached")

// ErrCircuitOpen is returned when the breaker is open.
var ErrCircuitOpen = errors.New("ratelimit: circuit open")

// Adaptive implements AIMD concurrency control: additive increase on success,
// multiplicative decrease on error or slow response. The limit moves between
// min and max in response to observed DB health.
type Adaptive struct {
	concurrency  atomic.Int64
	limit        atomic.Int64
	min, max     int64
	rttThreshold time.Duration
}

// NewAdaptive returns an Adaptive limiter. initial is the starting concurrency
// limit; it moves within [min, max]. rttThreshold is the latency above which a
// response is considered "slow" and triggers a decrease.
func NewAdaptive(initial, min, max int64, rttThreshold time.Duration) *Adaptive {
	a := &Adaptive{min: min, max: max, rttThreshold: rttThreshold}
	a.limit.Store(initial)
	return a
}

// Acquire reserves a concurrency slot. It returns ErrRateLimited if the current
// concurrency is at the limit.
func (a *Adaptive) Acquire() error {
	c := a.concurrency.Add(1)
	if c > a.limit.Load() {
		a.concurrency.Add(-1)
		return ErrRateLimited
	}
	return nil
}

// Release frees a concurrency slot.
func (a *Adaptive) Release() {
	a.concurrency.Add(-1)
}

// OnResult adjusts the limit: multiplicative decrease on error or slow RTT,
// additive increase otherwise.
func (a *Adaptive) OnResult(err error, d time.Duration) {
	if err != nil || (a.rttThreshold > 0 && d > a.rttThreshold) {
		next := a.limit.Load() / 2
		if next < a.min {
			next = a.min
		}
		a.limit.Store(next)
		return
	}
	next := a.limit.Load() + 1
	if next > a.max {
		next = a.max
	}
	a.limit.Store(next)
}

// Limit returns the current concurrency limit (for introspection/testing).
func (a *Adaptive) Limit() int64 { return a.limit.Load() }

// Breaker is a simple circuit breaker: after threshold consecutive failures it
// opens for cooldown, then allows a half-open probe.
type Breaker struct {
	failures  atomic.Int64
	threshold int64
	cooldown  time.Duration
	openUntil atomic.Int64 // unix nano
}

// NewBreaker returns a Breaker that opens after threshold consecutive failures,
// staying open for cooldown.
func NewBreaker(threshold int64, cooldown time.Duration) *Breaker {
	return &Breaker{threshold: threshold, cooldown: cooldown}
}

// Allow returns ErrCircuitOpen if the breaker is open and the cooldown has not
// elapsed.
func (b *Breaker) Allow() error {
	until := b.openUntil.Load()
	if until == 0 {
		return nil
	}
	if time.Now().UnixNano() < until {
		return ErrCircuitOpen
	}
	return nil // half-open: allow a probe
}

// Record updates failure count. A success resets it; a failure that crosses the
// threshold opens the breaker for cooldown.
func (b *Breaker) Record(success bool) {
	if success {
		b.failures.Store(0)
		b.openUntil.Store(0)
		return
	}
	f := b.failures.Add(1)
	if f >= b.threshold {
		b.openUntil.Store(time.Now().Add(b.cooldown).UnixNano())
	}
}

// IsOpen reports whether the breaker is currently open.
func (b *Breaker) IsOpen() bool {
	until := b.openUntil.Load()
	return until != 0 && time.Now().UnixNano() < until
}
