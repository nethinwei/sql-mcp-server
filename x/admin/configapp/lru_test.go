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

// A build that failed as its caller's request ended is built again for
// those that waited for it; another failure is their answer too.
func TestLRUWaitersBuildAgainAfterTheCallerLeft(t *testing.T) {
	for _, c := range []struct {
		weight, builds int
	}{{buildEnded, 2}, {-1, 1}} {
		cache := newLRU[int](10)
		started, release := make(chan struct{}), make(chan struct{})
		var mu sync.Mutex
		builds := 0
		go cache.get("k", func() (int, int) {
			mu.Lock()
			builds++
			mu.Unlock()
			close(started)
			<-release
			return 0, c.weight
		})
		<-started
		got := make(chan int)
		go func() {
			got <- cache.get("k", func() (int, int) {
				mu.Lock()
				builds++
				mu.Unlock()
				return 7, 1
			})
		}()
		time.Sleep(50 * time.Millisecond) // the second waits for the first build
		close(release)
		v := <-got
		mu.Lock()
		if builds != c.builds || c.builds == 2 && v != 7 {
			t.Errorf("weight %d: %d builds, value %d", c.weight, builds, v)
		}
		mu.Unlock()
	}
}

// An expired preview is let go when previews are next stored or read, not
// held until 32 newer ones push it out.
func TestPreviewsLetExpiredOnesGo(t *testing.T) {
	var k previews
	t0 := time.Unix(1000, 0)
	k.put(&Preview{PlanID: "a", owner: "root", at: t0})
	k.put(&Preview{PlanID: "b", owner: "root", at: t0.Add(previewTTL)})
	if len(k.all) != 1 || k.all[0].PlanID != "b" {
		t.Fatalf("kept %d previews", len(k.all))
	}
	if _, ok := k.get("b", "root", t0.Add(2*previewTTL)); ok || len(k.all) != 0 {
		t.Fatalf("an expired preview is served or kept: %d", len(k.all))
	}
}

// The decoded schemas of a source no one reads any more are let go once
// another is read sourceIdle later.
func TestScansForgetIdleSources(t *testing.T) {
	s := &Scans{cache: map[scanKey]keptSchema{}, usedAt: map[string]time.Time{}, built: map[string]builtCatalog{}}
	t0 := time.Unix(1000, 0)
	s.cache[scanKey{"old", "public"}], s.built["old"] = keptSchema{}, builtCatalog{}
	s.forgetIdleLocked("old", t0)
	s.forgetIdleLocked("new", t0.Add(sourceIdle/2))
	if _, ok := s.cache[scanKey{"old", "public"}]; !ok {
		t.Fatal("a source read lately was let go")
	}
	s.forgetIdleLocked("new", t0.Add(sourceIdle))
	if _, ok := s.cache[scanKey{"old", "public"}]; ok || len(s.built) != 0 || len(s.usedAt) != 1 {
		t.Fatalf("an idle source is kept: %d schemas, %d catalogs", len(s.cache), len(s.built))
	}
}
