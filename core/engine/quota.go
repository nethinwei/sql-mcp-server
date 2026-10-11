package engine

import (
	"sync/atomic"

	"github.com/nethinwei/sql-mcp-server/core/bus"
	"github.com/nethinwei/sql-mcp-server/core/ratelimit"
)

// Quota is what bounds IO across engines: the IO slots, the adaptive limiter,
// the request-rate limiter and the circuit breaker. Engines of successive
// configurations of one process share it, so a reload never lets the old and
// the new configuration together exceed the IO pool, and Update applies new
// limits to the work already running.
type Quota struct {
	io      *bus.Semaphore
	limiter atomic.Pointer[ratelimit.Adaptive]
	rps     atomic.Pointer[ratelimit.TokenBucket]
	breaker atomic.Pointer[ratelimit.Breaker]
}

// NewQuota returns a quota of io slots and no limiter, rate limit or breaker.
func NewQuota(io int) *Quota {
	return NewQuotaOn(bus.NewSemaphore(io))
}

// NewQuotaOn returns a quota whose IO slots are io, such as the IO semaphore
// of the process's bus, and no limiter, rate limit or breaker.
func NewQuotaOn(io *bus.Semaphore) *Quota {
	return &Quota{io: io}
}

// Update sets the IO slots and replaces the limiter, rate limiter and
// breaker (nil turns one off). Work holding a slot keeps it; when the slots
// shrink, new work waits until the running work fits.
func (q *Quota) Update(io int, limiter *ratelimit.Adaptive, rps *ratelimit.TokenBucket, breaker *ratelimit.Breaker) {
	q.io.Resize(io)
	q.limiter.Store(limiter)
	q.rps.Store(rps)
	q.breaker.Store(breaker)
}
