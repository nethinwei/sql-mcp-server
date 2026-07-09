package cache

import (
	"context"
	"sync"
	"time"
)

// Key identifies a cached entry: the entity and the parameterized SQL + args.
type Key struct {
	Entity string
	SQL    string
	Args   string
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

// TTLCache stores values with a TTL, evaluated lazily on Get.
type TTLCache[T any] struct {
	ttl time.Duration
	m   sync.Map
}

// NewTTLCache returns a TTLCache with the given time-to-live.
func NewTTLCache[T any](ttl time.Duration) *TTLCache[T] {
	return &TTLCache[T]{ttl: ttl}
}

// Get returns the value if present and unexpired.
func (c *TTLCache[T]) Get(_ context.Context, k Key) (T, bool) {
	v, ok := c.m.Load(k)
	if !ok {
		var z T
		return z, false
	}
	e := v.(entry[T])
	if time.Now().After(e.exp) {
		c.m.Delete(k)
		var z T
		return z, false
	}
	return e.v, true
}

// Set stores a value with the configured TTL.
func (c *TTLCache[T]) Set(_ context.Context, k Key, v T) error {
	c.m.Store(k, entry[T]{v: v, exp: time.Now().Add(c.ttl)})
	return nil
}

// Invalidate removes all entries whose Key.Entity matches.
func (c *TTLCache[T]) Invalidate(entity string) error {
	c.m.Range(func(k, _ any) bool {
		if k.(Key).Entity == entity {
			c.m.Delete(k)
		}
		return true
	})
	return nil
}
