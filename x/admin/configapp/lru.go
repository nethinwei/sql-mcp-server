package configapp

import (
	"container/list"
	"context"
	"errors"
	"sync"
)

// lru keeps values by key within a budget of their weights (bytes), the most
// recently used kept longest. Concurrent misses of a key build it once: the
// others wait for that build.
type lru[V any] struct {
	budget int
	mu     sync.Mutex
	used   int
	// order lists the kept entries, most recently used last; byKey finds
	// each one's place in it.
	order   *list.List
	byKey   map[string]*list.Element
	pending map[string]*lruCall[V]
}

type lruEntry[V any] struct {
	key    string
	value  V
	weight int
}

type lruCall[V any] struct {
	done  chan struct{}
	value V
	// failed: the build failed for its caller alone (see buildEnded), so
	// those waiting build it again.
	failed bool
}

func newLRU[V any](budget int) *lru[V] {
	return &lru[V]{budget: budget, order: list.New(), byKey: map[string]*list.Element{},
		pending: map[string]*lruCall[V]{}}
}

// buildEnded is the weight of a build that failed as its caller's request
// ended: not kept, and built again for those that waited for it.
const buildEnded = -2

// get returns the value of key, building it (with its weight) when it is
// not kept; a negative weight is not kept (a build that failed), its value
// shared with those that waited for it but for buildEnded.
func (c *lru[V]) get(key string, build func() (V, int)) V {
	for {
		c.mu.Lock()
		if el, ok := c.byKey[key]; ok {
			c.order.MoveToBack(el)
			c.mu.Unlock()
			return el.Value.(*lruEntry[V]).value
		}
		call, ok := c.pending[key]
		if !ok {
			return c.build(key, build) // unlocks c.mu
		}
		c.mu.Unlock()
		<-call.done
		if !call.failed {
			return call.value
		}
	}
}

// build builds the value of key for the calls waiting for it; c.mu is held,
// and unlocked once the build is registered.
func (c *lru[V]) build(key string, build func() (V, int)) V {
	call := &lruCall[V]{done: make(chan struct{})}
	c.pending[key] = call
	c.mu.Unlock()
	weight := 0
	defer func() {
		c.mu.Lock()
		delete(c.pending, key)
		if weight > 0 {
			c.keep(&lruEntry[V]{key: key, value: call.value, weight: weight})
		}
		call.failed = weight == buildEnded
		c.mu.Unlock()
		close(call.done)
	}()
	call.value, weight = build()
	if weight >= 0 {
		weight = max(weight, 1)
	}
	return call.value
}

// failedWeight is the weight of a build that failed with err: buildEnded
// when its caller's request ended.
func failedWeight(err error) int {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return buildEnded
	}
	return -1
}

// keep adds an entry, evicting the least recently used ones past the budget
// (never the entry itself).
func (c *lru[V]) keep(e *lruEntry[V]) {
	if old, ok := c.byKey[e.key]; ok {
		c.used -= c.order.Remove(old).(*lruEntry[V]).weight
	}
	c.byKey[e.key] = c.order.PushBack(e)
	c.used += e.weight
	for c.used > c.budget && c.order.Len() > 1 {
		oldest := c.order.Remove(c.order.Front()).(*lruEntry[V])
		delete(c.byKey, oldest.key)
		c.used -= oldest.weight
	}
}
