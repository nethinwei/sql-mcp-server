package admin

import (
	"context"
	"sync/atomic"
	"time"
)

// Password verification is memory-hard (argon2id, 64 MiB per attempt, the
// same for unknown accounts). At most maxConcurrentLogins attempts verify at
// once and at most maxQueuedLogins wait, each for up to loginQueueWait; any
// further attempt is rejected before doing work. This bounds memory and the
// global attempt rate whatever names or connections a caller uses.
const (
	maxConcurrentLogins = 2
	maxQueuedLogins     = 16
	loginQueueWait      = 5 * time.Second
)

type loginGate struct {
	slots  chan struct{}
	queued atomic.Int32
}

func newLoginGate(concurrent int) *loginGate {
	return &loginGate{slots: make(chan struct{}, concurrent)}
}

// enter waits for a verification slot; false means the caller must not
// verify. A true result must be paired with leave.
func (g *loginGate) enter(ctx context.Context) bool {
	select {
	case g.slots <- struct{}{}:
		return true
	default:
	}
	if g.queued.Add(1) > maxQueuedLogins {
		g.queued.Add(-1)
		return false
	}
	defer g.queued.Add(-1)
	ctx, cancel := context.WithTimeout(ctx, loginQueueWait)
	defer cancel()
	select {
	case g.slots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (g *loginGate) leave() {
	<-g.slots
}
