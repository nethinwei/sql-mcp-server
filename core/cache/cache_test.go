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
	if err := c.Set(ctx, Key{Database: "db", Relation: "u", SQL: "s"}, 42); err != nil {
		t.Fatal(err)
	}
	v, ok := c.Get(ctx, Key{Database: "db", Relation: "u", SQL: "s"})
	if !ok || v != 42 {
		t.Fatalf("got %v, %v", v, ok)
	}
}

func TestTTLCacheExpire(t *testing.T) {
	t.Parallel()
	c := NewTTLCache[int](time.Millisecond, 0)
	ctx := context.Background()
	_ = c.Set(ctx, Key{Database: "db", Relation: "u"}, 1)
	time.Sleep(5 * time.Millisecond)
	if _, ok := c.Get(ctx, Key{Database: "db", Relation: "u"}); ok {
		t.Fatal("expected expired miss")
	}
}

func TestTTLCacheInvalidate(t *testing.T) {
	t.Parallel()
	c := NewTTLCache[int](time.Minute, 0)
	ctx := context.Background()
	_ = c.Set(ctx, Key{Database: "db", Relation: "u", SQL: "a"}, 1)
	_ = c.Set(ctx, Key{Database: "db", Relation: "u", SQL: "b"}, 2)
	_ = c.Set(ctx, Key{Database: "db", Relation: "v", SQL: "a"}, 3)
	derived := Key{Database: "db", SQL: "view"}
	otherDB := Key{Database: "other", Relation: "u", SQL: "a"}
	_ = c.Set(ctx, derived, 4)
	_ = c.Set(ctx, otherDB, 5)
	if err := c.Invalidate("db", "u"); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get(ctx, Key{Database: "db", Relation: "u", SQL: "a"}); ok {
		t.Fatal("u.a should be invalidated")
	}
	if _, ok := c.Get(ctx, derived); ok {
		t.Fatal("data derived from the database's relations should be invalidated by any write to it")
	}
	if _, ok := c.Get(ctx, Key{Database: "db", Relation: "v", SQL: "a"}); !ok {
		t.Fatal("v.a should remain")
	}
	if _, ok := c.Get(ctx, otherDB); !ok {
		t.Fatal("the same relation name on another database should remain")
	}
	if err := c.Invalidate("db", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get(ctx, Key{Database: "db", Relation: "v", SQL: "a"}); ok {
		t.Fatal("an empty relation invalidates the whole database")
	}
}

func TestTTLCacheMaxSize(t *testing.T) {
	t.Parallel()
	c := NewTTLCache[int](time.Minute, 2)
	ctx := context.Background()
	_ = c.Set(ctx, Key{Database: "db", Relation: "u", SQL: "a"}, 1)
	_ = c.Set(ctx, Key{Database: "db", Relation: "u", SQL: "b"}, 2)
	_ = c.Set(ctx, Key{Database: "db", Relation: "u", SQL: "c"}, 3) // over cap -> evicts one
	count := 0
	for _, sql := range []string{"a", "b", "c"} {
		k := Key{Database: "db", Relation: "u", SQL: sql}
		if _, ok := c.Get(ctx, k); ok {
			count++
		}
	}
	if count > 2 {
		t.Fatalf("maxSize=2 but %d entries present", count)
	}
}

func TestTTLCacheDefaultCapacityIsBounded(t *testing.T) {
	t.Parallel()
	c := NewTTLCache[int](time.Minute, 0)
	ctx := context.Background()
	for i := 0; i < defaultMaxSize+10; i++ {
		if err := c.Set(ctx, Key{SQL: string(rune(i))}, i); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(c.m); got != defaultMaxSize {
		t.Fatalf("entries = %d, want %d", got, defaultMaxSize)
	}
}

func TestTTLCacheSetRemovesAllExpiredEntries(t *testing.T) {
	t.Parallel()
	c := NewTTLCache[int](time.Millisecond, 3)
	ctx := context.Background()
	_ = c.Set(ctx, Key{SQL: "a"}, 1)
	_ = c.Set(ctx, Key{SQL: "b"}, 2)
	time.Sleep(5 * time.Millisecond)
	_ = c.Set(ctx, Key{SQL: "c"}, 3)
	if got := len(c.m); got != 1 {
		t.Fatalf("entries after expiry cleanup = %d, want 1", got)
	}
}

func TestNoopCache(t *testing.T) {
	t.Parallel()
	var n NoopCache[int]
	if _, ok := n.Get(context.Background(), Key{}); ok {
		t.Fatal("noop should miss")
	}
}
