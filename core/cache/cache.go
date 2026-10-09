package cache

import (
	"container/heap"
	"context"
	"sync"
	"time"
)

const defaultMaxSize = 1024

// Key identifies a cached entry: the relation it reads (Relation of
// Database; empty for data derived from other relations of the database,
// such as a view), the statement, and Scope, which separates authorization
// and row-level identities.
type Key struct {
	// Database is the physical database the entry reads: invalidation
	// matches it across datasources and configuration generations.
	Database string
	Relation string
	// Datasource and Generation keep lookups apart: datasources may reach
	// one database through accounts that see different rows, and a reloaded
	// configuration may read it under different rules.
	Datasource string
	Generation uint64
	SQL        string
	Args       string
	Scope      string
}

// Cache is a generic read cache with per-relation invalidation.
type Cache[T any] interface {
	Get(ctx context.Context, k Key) (T, bool)
	// Stamp returns how many times database was invalidated. A reader takes
	// it before querying and passes it to Set, which stores nothing when the
	// database was invalidated meanwhile: the result may predate the write.
	Stamp(database string) uint64
	Set(ctx context.Context, k Key, v T, stamp uint64) error
	// Invalidate drops the entries reading relation of database and every
	// derived entry of database; an empty relation drops all of database.
	Invalidate(database, relation string) error
}

// NoopCache is a Cache that stores nothing.
type NoopCache[T any] struct{}

// Get always misses.
func (NoopCache[T]) Get(_ context.Context, _ Key) (T, bool) { var z T; return z, false }

// Stamp is always 0.
func (NoopCache[T]) Stamp(string) uint64 { return 0 }

// Set is a no-op.
func (NoopCache[T]) Set(_ context.Context, _ Key, _ T, _ uint64) error { return nil }

// Invalidate is a no-op.
func (NoopCache[T]) Invalidate(_, _ string) error { return nil }

// InvalidateOnly is a Cache that reads and stores nothing but passes
// invalidations on to Cache: a configuration with caching off still
// invalidates what the configurations sharing that cache stored.
type InvalidateOnly[T any] struct{ Cache Cache[T] }

// Get always misses.
func (InvalidateOnly[T]) Get(_ context.Context, _ Key) (T, bool) { var z T; return z, false }

// Stamp implements Cache.
func (c InvalidateOnly[T]) Stamp(database string) uint64 { return c.Cache.Stamp(database) }

// Set is a no-op.
func (InvalidateOnly[T]) Set(_ context.Context, _ Key, _ T, _ uint64) error { return nil }

// Invalidate passes the invalidation on.
func (c InvalidateOnly[T]) Invalidate(database, relation string) error {
	return c.Cache.Invalidate(database, relation)
}

type entry[T any] struct {
	key Key
	v   T
	exp time.Time
	at  int // position in the expiry heap
}

// expiries is a min-heap of entries by expiry.
type expiries[T any] []*entry[T]

func (h expiries[T]) Len() int           { return len(h) }
func (h expiries[T]) Less(i, j int) bool { return h[i].exp.Before(h[j].exp) }
func (h expiries[T]) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].at, h[j].at = i, j
}
func (h *expiries[T]) Push(x any) {
	e := x.(*entry[T])
	e.at = len(*h)
	*h = append(*h, e)
}
func (h *expiries[T]) Pop() any {
	old := *h
	e := old[len(old)-1]
	old[len(old)-1] = nil // the backing array must not keep the value alive
	*h = old[:len(old)-1]
	return e
}

// TTLCache stores values with a TTL. Entries are indexed by database, so an
// invalidation touches only that database's entries, and kept in a heap by
// expiry, so Set drops the expired entries and, over the cap, the one closest
// to expiry without scanning the others.
type TTLCache[T any] struct {
	mu         sync.Mutex
	ttl        time.Duration
	maxSize    int
	m          map[Key]*entry[T]
	byDatabase map[string]map[Key]struct{}
	expiry     expiries[T]
	stamps     map[string]uint64
}

// NewTTLCache returns a TTLCache. maxSize <= 0 uses a safe default capacity.
func NewTTLCache[T any](ttl time.Duration, maxSize int) *TTLCache[T] {
	c := &TTLCache[T]{m: map[Key]*entry[T]{}, byDatabase: map[string]map[Key]struct{}{}, stamps: map[string]uint64{}}
	c.UpdateLimits(ttl, maxSize)
	return c
}

// UpdateLimits applies a new TTL to entries stored from now on and a new cap,
// evicting the entries closest to expiry when the cap shrank.
func (c *TTLCache[T]) UpdateLimits(ttl time.Duration, maxSize int) {
	if maxSize <= 0 {
		maxSize = defaultMaxSize
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ttl, c.maxSize = ttl, maxSize
	for len(c.m) > c.maxSize {
		c.removeLocked(c.expiry[0])
	}
}

// Get returns the value if present and unexpired.
func (c *TTLCache[T]) Get(_ context.Context, k Key) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok || time.Now().After(e.exp) {
		if ok {
			c.removeLocked(e)
		}
		var z T
		return z, false
	}
	return e.v, true
}

// Stamp implements Cache.
func (c *TTLCache[T]) Stamp(database string) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stamps[database]
}

// Set stores a value with the configured TTL unless k's database was
// invalidated since stamp, evicting if over maxSize.
func (c *TTLCache[T]) Set(_ context.Context, k Key, v T, stamp uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stamps[k.Database] != stamp {
		return nil
	}
	now := time.Now()
	for len(c.expiry) > 0 && now.After(c.expiry[0].exp) {
		c.removeLocked(c.expiry[0])
	}
	if e, ok := c.m[k]; ok {
		e.v, e.exp = v, now.Add(c.ttl)
		heap.Fix(&c.expiry, e.at)
		return nil
	}
	if len(c.m) >= c.maxSize {
		c.removeLocked(c.expiry[0])
	}
	e := &entry[T]{key: k, v: v, exp: now.Add(c.ttl)}
	c.m[k] = e
	heap.Push(&c.expiry, e)
	if c.byDatabase[k.Database] == nil {
		c.byDatabase[k.Database] = map[Key]struct{}{}
	}
	c.byDatabase[k.Database][k] = struct{}{}
	return nil
}

// Invalidate implements Cache.
func (c *TTLCache[T]) Invalidate(database, relation string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stamps[database]++
	for k := range c.byDatabase[database] {
		if relation == "" || k.Relation == "" || k.Relation == relation {
			c.removeLocked(c.m[k])
		}
	}
	return nil
}

// DropGeneration drops the entries a configuration generation stored: once
// it serves no request, nothing reads them.
func (c *TTLCache[T]) DropGeneration(generation uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.m {
		if k.Generation == generation {
			c.removeLocked(e)
		}
	}
}

func (c *TTLCache[T]) removeLocked(e *entry[T]) {
	delete(c.m, e.key)
	heap.Remove(&c.expiry, e.at)
	if keys := c.byDatabase[e.key.Database]; keys != nil {
		delete(keys, e.key)
		if len(keys) == 0 {
			delete(c.byDatabase, e.key.Database)
		}
	}
}
