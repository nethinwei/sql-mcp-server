package configapp

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Concurrent misses of a key build it once; past the budget the least
// recently used entries go, never the one just built.
func TestLRU(t *testing.T) {
	t.Parallel()
	c := newLRU[int](10)
	var builds atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if v := c.get("a", func() (int, int) {
				builds.Add(1)
				time.Sleep(20 * time.Millisecond)
				return 1, 4
			}); v != 1 {
				t.Errorf("a = %d", v)
			}
		})
	}
	wg.Wait()
	if builds.Load() != 1 {
		t.Fatalf("a was built %d times", builds.Load())
	}
	c.get("b", func() (int, int) { return 2, 4 })
	c.get("a", func() (int, int) { t.Fatal("a is kept"); return 0, 0 })
	c.get("c", func() (int, int) { return 3, 4 }) // past the budget: b, used least recently, goes
	rebuilt := false
	c.get("b", func() (int, int) { rebuilt = true; return 2, 4 })
	if !rebuilt {
		t.Fatal("b was kept past the budget")
	}
	if v := c.get("huge", func() (int, int) { return 9, 100 }); v != 9 || c.order.Len() != 1 || len(c.byKey) != 1 {
		t.Fatalf("an entry past the budget alone: %d, %d entries", v, c.order.Len())
	}
}

// A build with a negative weight (one that failed) is not kept: the next
// get builds again.
func TestLRUKeepsNoFailedBuild(t *testing.T) {
	c := newLRU[int](10)
	builds := 0
	failing := func() (int, int) { builds++; return 0, -1 }
	c.get("k", failing)
	c.get("k", failing)
	if builds != 2 {
		t.Fatalf("builds = %d, want 2", builds)
	}
	c.get("k", func() (int, int) { builds++; return 7, 0 })
	if v := c.get("k", failing); v != 7 || builds != 3 {
		t.Fatalf("value %d after %d builds: an empty build (weight 0) is kept", v, builds)
	}
}
