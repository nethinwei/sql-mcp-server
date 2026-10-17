package configapp

import (
	"slices"
	"sync"
)

// lru keeps values by key within a budget of their weights (bytes), the most
// recently used kept longest. Concurrent misses of a key build it once: the
// others wait for that build.
type lru[V any] struct {
	budget int
	mu     sync.Mutex
	used   int
	// entries are the kept values, most recently used last.
	entries []*lruEntry[V]
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
}

func newLRU[V any](budget int) *lru[V] {
	return &lru[V]{budget: budget, pending: map[string]*lruCall[V]{}}
}

// get returns the value of key, building it (with its weight) when it is
// not kept; a negative weight is not kept (a build that failed).
func (c *lru[V]) get(key string, build func() (V, int)) V {
	c.mu.Lock()
	for i, e := range c.entries {
		if e.key == key {
			c.entries = append(slices.Delete(c.entries, i, i+1), e)
			c.mu.Unlock()
			return e.value
		}
	}
	if call, ok := c.pending[key]; ok {
		c.mu.Unlock()
		<-call.done
		return call.value
	}
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
		c.mu.Unlock()
		close(call.done)
	}()
	call.value, weight = build()
	if weight >= 0 {
		weight = max(weight, 1)
	}
	return call.value
}

// keep adds an entry, evicting the least recently used ones past the budget
// (never the entry itself).
func (c *lru[V]) keep(e *lruEntry[V]) {
	c.entries = append(c.entries, e)
	c.used += e.weight
	for c.used > c.budget && len(c.entries) > 1 {
		c.used -= c.entries[0].weight
		c.entries = c.entries[1:]
	}
}
