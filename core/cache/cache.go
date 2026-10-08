package cache

import (
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
	Database string
	Relation string
	SQL      string
	Args     string
	Scope    string
}

// Cache is a generic read cache with per-relation invalidation.
type Cache[T any] interface {
	Get(ctx context.Context, k Key) (T, bool)
	Set(ctx context.Context, k Key, v T) error
	// Invalidate drops the entries reading relation of database and every
	// derived entry of database; an empty relation drops all of database.
	Invalidate(database, relation string) error
}

// NoopCache is a Cache that stores nothing.
type NoopCache[T any] struct{}

// Get always misses.
func (NoopCache[T]) Get(_ context.Context, _ Key) (T, bool) { var z T; return z, false }

// Set is a no-op.
func (NoopCache[T]) Set(_ context.Context, _ Key, _ T) error { return nil }

// Invalidate is a no-op.
func (NoopCache[T]) Invalidate(_, _ string) error { return nil }

type entry[T any] struct {
	v   T
	exp time.Time
}

// TTLCache stores values with a TTL, evaluated lazily on Get. Set removes all
// expired entries before evicting an arbitrary live entry to stay under the
// configured cap.
type TTLCache[T any] struct {
	ttl     time.Duration
	maxSize int
	mu      sync.Mutex
	m       map[Key]entry[T]
}

// NewTTLCache returns a TTLCache. maxSize <= 0 uses a safe default capacity.
func NewTTLCache[T any](ttl time.Duration, maxSize int) *TTLCache[T] {
	if maxSize <= 0 {
		maxSize = defaultMaxSize
	}
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
	now := time.Now()
	for ek, ev := range c.m {
		if now.After(ev.exp) {
			delete(c.m, ek)
		}
	}
	if _, exists := c.m[k]; !exists && len(c.m) >= c.maxSize {
		for ek := range c.m {
			delete(c.m, ek)
			break
		}
	}
	c.m[k] = entry[T]{v: v, exp: now.Add(c.ttl)}
	return nil
}

// Invalidate implements Cache.
func (c *TTLCache[T]) Invalidate(database, relation string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.m {
		if k.Database == database && (relation == "" || k.Relation == "" || k.Relation == relation) {
			delete(c.m, k)
		}
	}
	return nil
}
