package cache

import (
	"context"
	"sync"
	"time"
)

// Key identifies a cached entry. Scope separates authorization/RLS identities.
type Key struct {
	Entity string
	SQL    string
	Args   string
	Scope  string
}

// Cache is a generic read cache with per-table invalidation.
type Cache[T any] interface {
	Get(ctx context.Context, k Key) (T, bool)
	Set(ctx context.Context, k Key, v T) error
	Invalidate(entity string) error
}

// NoopCache is a Cache that stores nothing.
type NoopCache[T any] struct{}

// Get always misses.
func (NoopCache[T]) Get(_ context.Context, _ Key) (T, bool) { var z T; return z, false }

// Set is a no-op.
func (NoopCache[T]) Set(_ context.Context, _ Key, _ T) error { return nil }

// Invalidate is a no-op.
func (NoopCache[T]) Invalidate(_ string) error { return nil }

type entry[T any] struct {
	v   T
	exp time.Time
}

// TTLCache stores values with a TTL, evaluated lazily on Get. When maxSize > 0,
// Set evicts (expired first, then arbitrary) to stay under the cap.
type TTLCache[T any] struct {
	ttl     time.Duration
	maxSize int
	mu      sync.Mutex
	m       map[Key]entry[T]
}

// NewTTLCache returns a TTLCache. maxSize <= 0 means unbounded.
func NewTTLCache[T any](ttl time.Duration, maxSize int) *TTLCache[T] {
	return &TTLCache[T]{ttl: ttl, maxSize: maxSize, m: make(map[Key]entry[T])}
}

// Get returns the value if present and unexpired.
func (c *TTLCache[T]) Get(_ context.Context, k Key) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok {
		var z T
		return z, false
	}
	if time.Now().After(e.exp) {
		delete(c.m, k)
		var z T
		return z, false
	}
	return e.v, true
}

// Set stores a value with the configured TTL, evicting if over maxSize.
func (c *TTLCache[T]) Set(_ context.Context, k Key, v T) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.maxSize > 0 {
		if _, exists := c.m[k]; !exists && len(c.m) >= c.maxSize {
			evicted := false
			for ek, ev := range c.m {
				if time.Now().After(ev.exp) {
					delete(c.m, ek)
					evicted = true
					break
				}
			}
			if !evicted {
				for ek := range c.m {
					delete(c.m, ek)
					break
				}
			}
		}
	}
	c.m[k] = entry[T]{v: v, exp: time.Now().Add(c.ttl)}
	return nil
}

// Invalidate removes all entries whose Key.Entity matches.
func (c *TTLCache[T]) Invalidate(entity string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.m {
		if k.Entity == entity {
			delete(c.m, k)
		}
	}
	return nil
}
