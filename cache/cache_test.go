package cache

import (
	"context"
	"testing"
	"time"
)

func TestTTLCacheSetGet(t *testing.T) {
	t.Parallel()
	c := NewTTLCache[int](time.Minute, 0)
	ctx := context.Background()
	if err := c.Set(ctx, Key{Entity: "u", SQL: "s"}, 42); err != nil {
		t.Fatal(err)
	}
	v, ok := c.Get(ctx, Key{Entity: "u", SQL: "s"})
	if !ok || v != 42 {
		t.Fatalf("got %v, %v", v, ok)
	}
}

func TestTTLCacheExpire(t *testing.T) {
	t.Parallel()
	c := NewTTLCache[int](time.Millisecond, 0)
	ctx := context.Background()
	_ = c.Set(ctx, Key{Entity: "u"}, 1)
	time.Sleep(5 * time.Millisecond)
	if _, ok := c.Get(ctx, Key{Entity: "u"}); ok {
		t.Fatal("expected expired miss")
	}
}

func TestTTLCacheInvalidate(t *testing.T) {
	t.Parallel()
	c := NewTTLCache[int](time.Minute, 0)
	ctx := context.Background()
	_ = c.Set(ctx, Key{Entity: "u", SQL: "a"}, 1)
	_ = c.Set(ctx, Key{Entity: "u", SQL: "b"}, 2)
	_ = c.Set(ctx, Key{Entity: "v", SQL: "a"}, 3)
	if err := c.Invalidate("u"); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get(ctx, Key{Entity: "u", SQL: "a"}); ok {
		t.Fatal("u.a should be invalidated")
	}
	if _, ok := c.Get(ctx, Key{Entity: "v", SQL: "a"}); !ok {
		t.Fatal("v.a should remain")
	}
}

func TestTTLCacheMaxSize(t *testing.T) {
	t.Parallel()
	c := NewTTLCache[int](time.Minute, 2)
	ctx := context.Background()
	_ = c.Set(ctx, Key{Entity: "u", SQL: "a"}, 1)
	_ = c.Set(ctx, Key{Entity: "u", SQL: "b"}, 2)
	_ = c.Set(ctx, Key{Entity: "u", SQL: "c"}, 3) // over cap -> evicts one
	count := 0
	for _, k := range []Key{{Entity: "u", SQL: "a"}, {Entity: "u", SQL: "b"}, {Entity: "u", SQL: "c"}} {
		if _, ok := c.Get(ctx, k); ok {
			count++
		}
	}
	if count > 2 {
		t.Fatalf("maxSize=2 but %d entries present", count)
	}
}

func TestNoopCache(t *testing.T) {
	t.Parallel()
	var n NoopCache[int]
	if _, ok := n.Get(context.Background(), Key{}); ok {
		t.Fatal("noop should miss")
	}
}
