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
// min and max in response to observed DB health. A decrease answers the
// results of requests sent before it as well: those come back after it
// without decreasing again, so one overload halves the limit once.
type Adaptive struct {
	concurrency  atomic.Int64
	limit        atomic.Int64
	decreased    atomic.Int64 // unix nano of the last decrease
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

// OnResult adjusts the limit for a request that took d: multiplicative
// decrease on error or slow RTT, unless it was sent before the last decrease;
// additive increase otherwise.
func (a *Adaptive) OnResult(err error, d time.Duration) {
	now := time.Now().UnixNano()
	if err != nil || (a.rttThreshold > 0 && d > a.rttThreshold) {
		sent := now - int64(d)
		for {
			last := a.decreased.Load()
			if sent < last {
				return // already answered for
			}
			if a.decreased.CompareAndSwap(last, now) {
				break
			}
		}
		a.update(func(l int64) int64 { return max(l/2, a.min) })
		return
	}
	a.update(func(l int64) int64 { return min(l+1, a.max) })
}

// update sets the limit to next of it, atomically.
func (a *Adaptive) update(next func(int64) int64) {
	for {
		l := a.limit.Load()
		if a.limit.CompareAndSwap(l, next(l)) {
			return
		}
	}
}

// Limit returns the current concurrency limit (for introspection/testing).
func (a *Adaptive) Limit() int64 { return a.limit.Load() }

// Breaker is a circuit breaker: after threshold consecutive failures it opens
// for cooldown, then lets one probe through at a time (half-open) until a
// probe's result closes it, or reopens it.
type Breaker struct {
	failures  atomic.Int64
	threshold int64
	cooldown  time.Duration
	openUntil atomic.Int64 // unix nano; 0 when closed
	// probe is the probe through while half-open (0: none), numbered from
	// probes, so that only that request frees its place.
	probe  atomic.Uint64
	probes atomic.Uint64
	// epoch counts the breaker's openings and closings: a request let
	// through while closed carries it, and its outcome counts only while the
	// breaker has not changed state since.
	epoch atomic.Uint64
}

// probeTicket marks a half-open probe's ticket; any other ticket is the epoch
// a request was let through in.
const probeTicket = 1 << 63

// NewBreaker returns a Breaker that opens after threshold consecutive failures,
// staying open for cooldown.
func NewBreaker(threshold int64, cooldown time.Duration) *Breaker {
	return &Breaker{threshold: threshold, cooldown: cooldown}
}

// Allow returns ErrCircuitOpen if the breaker is open, or half-open with a
// probe already through. A request it lets through must end with Record, or
// with Release when its outcome says nothing of health, passing the ticket
// Allow returned.
func (b *Breaker) Allow() (ticket uint64, err error) {
	until := b.openUntil.Load()
	if until == 0 {
		return b.epoch.Load(), nil
	}
	if time.Now().UnixNano() < until {
		return 0, ErrCircuitOpen
	}
	ticket = b.probes.Add(1) | probeTicket
	if !b.probe.CompareAndSwap(0, ticket) {
		return 0, ErrCircuitOpen
	}
	return ticket, nil
}

// Release ends a request whose outcome says nothing of health (a cancelled
// one), ticket as Allow returned it: the probe frees its place, and any other
// request (one let through before the breaker opened, an earlier probe)
// leaves the current probe's alone.
func (b *Breaker) Release(ticket uint64) {
	if ticket&probeTicket != 0 {
		b.probe.CompareAndSwap(ticket, 0)
	}
}

// Record counts a request's outcome, ticket as Allow returned it. While
// closed, a success resets the count of failures and a failure opens the
// breaker at threshold consecutive ones. Once open, only the half-open
// probe's outcome counts: its success closes the breaker, its failure
// reopens it. A request let through before the breaker last opened or closed
// tells of the database as it was then, and changes nothing.
func (b *Breaker) Record(ticket uint64, success bool) {
	if b.openUntil.Load() != 0 {
		if ticket&probeTicket == 0 || !b.probe.CompareAndSwap(ticket, 0) {
			return
		}
		if success {
			b.failures.Store(0)
			b.epoch.Add(1) // before closing: a request let through now is new
			b.openUntil.Store(0)
		} else {
			b.openUntil.Store(time.Now().Add(b.cooldown).UnixNano())
		}
		return
	}
	if ticket != b.epoch.Load() {
		return
	}
	if success {
		b.failures.Store(0)
		return
	}
	if b.failures.Add(1) >= b.threshold {
		b.openUntil.Store(time.Now().Add(b.cooldown).UnixNano())
		b.probe.Store(0)
		b.epoch.Add(1)
	}
}

// IsOpen reports whether the breaker is currently open.
func (b *Breaker) IsOpen() bool {
	until := b.openUntil.Load()
	return until != 0 && time.Now().UnixNano() < until
}
