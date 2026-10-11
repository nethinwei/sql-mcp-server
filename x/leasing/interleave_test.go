package leasing

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/x/catalog"
)

// The interleaving model: a scripted store injects, at the nth move of a
// run, what can go wrong between a run and its store (the store fails, the
// answer of a made write is lost, another process takes the batch over,
// the batch is canceled, the store fails for good), and the invariants of
// leasing are checked whatever happened:
//
//   - the store never records a write by a run that lost the batch (the
//     store refuses it; the run then stops);
//   - an item's attempts count the moves to running, and the work ran at
//     most once per attempt, never after the run lost the batch;
//   - a batch that ended has no pending or running item left; one whose
//     run stopped without ending it is left as last recorded, for a
//     takeover;
//   - every run stops: nothing spins on a store that fails.

type fault int

const (
	failOnce    fault = iota + 1 // the move fails, the next one goes through
	loseReply                    // the move is made, its answer lost
	steal                        // another process takes the batch over first
	cancel                       // the batch is canceled first
	failForGood                  // the store fails from here on
)

// scripted is a BatchStore with faults at given moves of a run.
type scripted struct {
	catalog.BatchStore
	mu     sync.Mutex
	faults map[int]fault
	moves  int
	down   bool
	// writesAfterLoss counts the moves the store accepted from an owner that
	// no longer holds the batch: the invariant the store keeps.
	writesAfterLoss atomic.Int32
}

func (s *scripted) MoveItem(
	ctx context.Context, id, owner string, now time.Time, it catalog.BatchItem, from ...string,
) (bool, error) {
	s.mu.Lock()
	s.moves++
	f := s.faults[s.moves]
	down := s.down
	if f == failForGood {
		s.down = true
	}
	s.mu.Unlock()
	switch {
	case down || f == failForGood || f == failOnce:
		return false, errors.New("store unavailable")
	case f == steal:
		later := now.Add(time.Hour)
		_, _ = s.ClaimBatch(ctx, id, "thief.run", later, later)
	case f == cancel:
		_ = s.CancelBatch(ctx, id)
	}
	b, ok, _ := s.Batch(ctx, id)
	moved, err := s.BatchStore.MoveItem(ctx, id, owner, now, it, from...)
	if err == nil && moved && ok && b.Owner != owner {
		s.writesAfterLoss.Add(1)
	}
	if err == nil && f == loseReply {
		return false, errors.New("connection reset")
	}
	return moved, err
}

// counting works items, counting the runs of each, and fails the ones named
// in failing.
type counting struct {
	mu      sync.Mutex
	runs    map[string]int
	failing map[string]bool
}

func (c *counting) work(_ context.Context, _, schema string) (string, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.runs[schema]++
	if c.failing[schema] {
		return catalog.ItemFailed, "broken"
	}
	return catalog.ItemDone, ""
}

func TestInterleavings(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		schemas []string
		failing []string
		faults  map[int]fault
		ends    bool // whether the batch ends under this run
	}{
		{name: "no fault", schemas: []string{"a", "b"}, ends: true},
		{name: "a failed move is retried", schemas: []string{"a", "b"}, faults: map[int]fault{2: failOnce}, ends: true},
		{name: "a lost answer counts", schemas: []string{"a", "b"}, faults: map[int]fault{1: loseReply, 4: loseReply},
			ends: true},
		{name: "a lost answer of a retry", schemas: []string{"a"}, failing: []string{"a"},
			faults: map[int]fault{2: loseReply}, ends: true},
		{name: "taken over before the first move", schemas: []string{"a", "b"}, faults: map[int]fault{1: steal}},
		{name: "taken over between items", schemas: []string{"a", "b"}, faults: map[int]fault{3: steal}},
		{name: "taken over while an item runs", schemas: []string{"a"}, faults: map[int]fault{2: steal}},
		{name: "canceled between items", schemas: []string{"a", "b", "c"}, faults: map[int]fault{3: cancel}, ends: true},
		{name: "the store fails for good", schemas: []string{"a", "b"}, faults: map[int]fault{3: failForGood}},
		{name: "the store fails for good on a retry", schemas: []string{"a"}, failing: []string{"a"},
			faults: map[int]fault{2: failForGood}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := &scripted{BatchStore: catalog.NewMemoryStore(), faults: tc.faults}
			work := &counting{runs: map[string]int{}, failing: map[string]bool{}}
			for _, s := range tc.failing {
				work.failing[s] = true
			}
			opts := quick
			opts.Beat = 5 * time.Millisecond // notices a takeover or a cancel soon
			r := New(store, opts)
			t.Cleanup(r.Close)
			b := start(t, r, work.work, tc.schemas...)
			waitStopped(t, r, b.ID)
			checkInvariants(t, store, work, batchState(t, store, b.ID), tc.ends)
		})
	}
}

func checkInvariants(t *testing.T, store *scripted, work *counting, got catalog.Batch, ends bool) {
	t.Helper()
	if n := store.writesAfterLoss.Load(); n != 0 {
		t.Fatalf("%d writes by a run that lost the batch", n)
	}
	if got.FinishedAt.IsZero() == ends {
		t.Fatalf("ended = %v, want %v: %+v", !got.FinishedAt.IsZero(), ends, got)
	}
	work.mu.Lock()
	defer work.mu.Unlock()
	for _, it := range got.Items {
		runs := work.runs[it.Schema]
		if runs > it.Attempts || it.Attempts-runs > 1 {
			t.Fatalf("%s worked %d times in %d attempts: %+v", it.Schema, runs, it.Attempts, it)
		}
		if ends && (it.State == catalog.ItemPending || it.State == catalog.ItemRunning) {
			t.Fatalf("%s left %s in an ended batch: %+v", it.Schema, it.State, got.Items)
		}
		if it.State == catalog.ItemDone && (work.failing[it.Schema] || runs == 0) {
			t.Fatalf("%s done without a run that succeeded: %+v", it.Schema, it)
		}
	}
}
