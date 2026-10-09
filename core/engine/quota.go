package engine

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/nethinwei/sql-mcp-server/core/ratelimit"
)

// Quota is what bounds IO across engines: the IO slots, the adaptive limiter,
// the request-rate limiter and the circuit breaker. Engines of successive
// configurations of one process share it, so a reload never lets the old and
// the new configuration together exceed the IO pool, and Update applies new
// limits to the work already running.
type Quota struct {
	io      slots
	limiter atomic.Pointer[ratelimit.Adaptive]
	rps     atomic.Pointer[ratelimit.TokenBucket]
	breaker atomic.Pointer[ratelimit.Breaker]
}

// NewQuota returns a quota of io slots and no limiter, rate limit or breaker.
func NewQuota(io int) *Quota {
	q := &Quota{}
	q.io.resize(io)
	return q
}

// Update sets the IO slots and replaces the limiter, rate limiter and
// breaker (nil turns one off). Work holding a slot keeps it; when the slots
// shrink, new work waits until the running work fits.
func (q *Quota) Update(io int, limiter *ratelimit.Adaptive, rps *ratelimit.TokenBucket, breaker *ratelimit.Breaker) {
	q.io.resize(io)
	q.limiter.Store(limiter)
	q.rps.Store(rps)
	q.breaker.Store(breaker)
}

// slots is a counting semaphore whose size can change.
type slots struct {
	mu      sync.Mutex
	size    int
	used    int
	waiters []chan struct{}
}

func (s *slots) resize(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.size = n
	s.grantLocked()
}

func (s *slots) acquire(ctx context.Context) error {
	s.mu.Lock()
	if s.used < s.size && len(s.waiters) == 0 {
		s.used++
		s.mu.Unlock()
		return nil
	}
	granted := make(chan struct{})
	s.waiters = append(s.waiters, granted)
	s.mu.Unlock()
	select {
	case <-granted:
		return nil
	case <-ctx.Done():
		s.mu.Lock()
		defer s.mu.Unlock()
		for i, w := range s.waiters {
			if w == granted {
				s.waiters = append(s.waiters[:i], s.waiters[i+1:]...)
				return ctx.Err()
			}
		}
		// Granted as the context ended: hand the slot back.
		s.used--
		s.grantLocked()
		return ctx.Err()
	}
}

func (s *slots) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.used--
	s.grantLocked()
}

func (s *slots) grantLocked() {
	for s.used < s.size && len(s.waiters) > 0 {
		s.used++
		close(s.waiters[0])
		s.waiters = s.waiters[1:]
	}
}
