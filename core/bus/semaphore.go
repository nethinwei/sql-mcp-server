package bus

import (
	"context"
	"sync"
)

// Semaphore is a counting semaphore whose size can change. Waiters are
// granted in arrival order.
type Semaphore struct {
	mu      sync.Mutex
	size    int
	used    int
	waiters []chan struct{}
}

// NewSemaphore returns a semaphore of n slots.
func NewSemaphore(n int) *Semaphore {
	return &Semaphore{size: n}
}

// Resize sets the number of slots. Holders keep their slots; when the slots
// shrink, new work waits until the running work fits.
func (s *Semaphore) Resize(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.size = n
	s.grantLocked()
}

// TryAcquire takes a slot when one is free and nobody waits for it.
func (s *Semaphore) TryAcquire() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.used < s.size && len(s.waiters) == 0 {
		s.used++
		return true
	}
	return false
}

// Acquire takes a slot, waiting for one until ctx ends.
func (s *Semaphore) Acquire(ctx context.Context) error {
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

// Release frees a slot taken by Acquire or TryAcquire.
func (s *Semaphore) Release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.used--
	s.grantLocked()
}

// Stats returns the size, the slots in use and the number of waiters.
func (s *Semaphore) Stats() SlotStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SlotStats{Size: s.size, Used: s.used, Waiting: len(s.waiters)}
}

func (s *Semaphore) grantLocked() {
	for s.used < s.size && len(s.waiters) > 0 {
		s.used++
		close(s.waiters[0])
		s.waiters = s.waiters[1:]
	}
}
