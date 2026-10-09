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
	if err := c.Set(ctx, Key{Database: "db", Relation: "u", SQL: "s"}, 42, 0); err != nil {
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
	_ = c.Set(ctx, Key{Database: "db", Relation: "u"}, 1, 0)
	time.Sleep(5 * time.Millisecond)
	if _, ok := c.Get(ctx, Key{Database: "db", Relation: "u"}); ok {
		t.Fatal("expected expired miss")
	}
}

func TestTTLCacheInvalidate(t *testing.T) {
	t.Parallel()
	c := NewTTLCache[int](time.Minute, 0)
	ctx := context.Background()
	_ = c.Set(ctx, Key{Database: "db", Relation: "u", SQL: "a"}, 1, 0)
	_ = c.Set(ctx, Key{Database: "db", Relation: "u", SQL: "b"}, 2, 0)
	_ = c.Set(ctx, Key{Database: "db", Relation: "v", SQL: "a"}, 3, 0)
	derived := Key{Database: "db", SQL: "view"}
	otherDB := Key{Database: "other", Relation: "u", SQL: "a"}
	_ = c.Set(ctx, derived, 4, 0)
	_ = c.Set(ctx, otherDB, 5, 0)
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
	_ = c.Set(ctx, Key{Database: "db", Relation: "u", SQL: "a"}, 1, 0)
	_ = c.Set(ctx, Key{Database: "db", Relation: "u", SQL: "b"}, 2, 0)
	_ = c.Set(ctx, Key{Database: "db", Relation: "u", SQL: "c"}, 3, 0) // over cap -> evicts one
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
		if err := c.Set(ctx, Key{SQL: string(rune(i))}, i, 0); err != nil {
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
	_ = c.Set(ctx, Key{SQL: "a"}, 1, 0)
	_ = c.Set(ctx, Key{SQL: "b"}, 2, 0)
	time.Sleep(5 * time.Millisecond)
	_ = c.Set(ctx, Key{SQL: "c"}, 3, 0)
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

// A read that started before an invalidation does not store its result: it
// may predate the write.
func TestTTLCacheSetAfterInvalidationIsDropped(t *testing.T) {
	t.Parallel()
	c := NewTTLCache[int](time.Minute, 0)
	ctx := context.Background()
	k := Key{Database: "db", Relation: "u", SQL: "a"}
	stamp := c.Stamp("db")
	_ = c.Invalidate("db", "u")
	_ = c.Set(ctx, k, 1, stamp)
	if _, ok := c.Get(ctx, k); ok {
		t.Fatal("a result read before the invalidation must not be stored")
	}
	_ = c.Set(ctx, k, 2, c.Stamp("db"))
	if v, ok := c.Get(ctx, k); !ok || v != 2 {
		t.Fatalf("a result read after it = %v, %v", v, ok)
	}
}

// Invalidation matches the database across datasources and generations;
// lookups stay apart, and a generation's entries can be dropped.
func TestTTLCacheGenerations(t *testing.T) {
	t.Parallel()
	c := NewTTLCache[int](time.Minute, 0)
	ctx := context.Background()
	old := Key{Database: "db", Relation: "u", Datasource: "shop", Generation: 1}
	next := Key{Database: "db", Relation: "u", Datasource: "main", Generation: 2}
	_ = c.Set(ctx, old, 1, 0)
	if _, ok := c.Get(ctx, next); ok {
		t.Fatal("another generation must not read the entry")
	}
	_ = c.Set(ctx, next, 2, 0)
	_ = c.Invalidate("db", "u")
	if _, ok := c.Get(ctx, next); ok {
		t.Fatal("a write through any datasource or generation invalidates the database")
	}
	_ = c.Set(ctx, old, 1, c.Stamp("db"))
	_ = c.Set(ctx, next, 2, c.Stamp("db"))
	c.DropGeneration(1)
	if _, ok := c.Get(ctx, old); ok {
		t.Fatal("a dropped generation's entries must be gone")
	}
	if _, ok := c.Get(ctx, next); !ok {
		t.Fatal("other generations keep theirs")
	}
}

// Over the cap, the entry closest to expiry goes; a smaller cap evicts at once.
func TestTTLCacheEvictsClosestToExpiry(t *testing.T) {
	t.Parallel()
	c := NewTTLCache[int](time.Hour, 2)
	ctx := context.Background()
	_ = c.Set(ctx, Key{SQL: "first"}, 1, 0)
	c.UpdateLimits(2*time.Hour, 2)
	_ = c.Set(ctx, Key{SQL: "second"}, 2, 0)
	_ = c.Set(ctx, Key{SQL: "third"}, 3, 0)
	if _, ok := c.Get(ctx, Key{SQL: "first"}); ok {
		t.Fatal("the entry closest to expiry must be evicted")
	}
	c.UpdateLimits(time.Hour, 1)
	if len(c.m) != 1 {
		t.Fatalf("entries after shrinking the cap = %d", len(c.m))
	}
}

// Removed entries leave no reference in the expiry heap's backing array, so
// their values can be collected.
func TestTTLCacheReleasesRemovedValues(t *testing.T) {
	t.Parallel()
	c := NewTTLCache[[]byte](time.Minute, 0)
	ctx := context.Background()
	for _, sql := range []string{"a", "b", "c"} {
		_ = c.Set(ctx, Key{Database: "db", SQL: sql}, make([]byte, 1<<10), 0)
	}
	_ = c.Invalidate("db", "")
	for i, e := range c.expiry[:cap(c.expiry)] {
		if e != nil {
			t.Fatalf("slot %d still holds a removed entry", i)
		}
	}
}
