package leasing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/bus"
	"github.com/nethinwei/sql-mcp-server/x/catalog"
)

// quick are Options for tests: short waits, one item at a time.
var quick = Options{Parallel: 1, Backoff: 10 * time.Millisecond, MoveBackoff: time.Millisecond}

func items(schemas ...string) []catalog.BatchItem {
	out := make([]catalog.BatchItem, 0, len(schemas))
	for _, s := range schemas {
		out = append(out, catalog.BatchItem{Schema: s, State: catalog.ItemPending})
	}
	return out
}

func start(t *testing.T, r *Runner, work Work, schemas ...string) catalog.Batch {
	t.Helper()
	b, created, err := r.Start(context.Background(), catalog.Batch{Datasource: "shop", Items: items(schemas...)}, 4, work)
	if err != nil || !created {
		t.Fatalf("start = %v, created %v", err, created)
	}
	return b
}

// blocked works until ctx ends.
func blocked(ctx context.Context, _, _ string) (string, string) {
	<-ctx.Done()
	return "", ""
}

func succeeds(context.Context, string, string) (string, string) { return catalog.ItemDone, "" }

func resolving(work Work) Resolver {
	return func(context.Context, string) (Work, error) { return work, nil }
}

func batchState(t *testing.T, store catalog.BatchStore, id string) catalog.Batch {
	t.Helper()
	b, ok, err := store.Batch(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("batch %s: %v %v", id, ok, err)
	}
	return b
}

func waitFinished(t *testing.T, store catalog.BatchStore, id string) catalog.Batch {
	t.Helper()
	return waitFor(t, store, id, func(b catalog.Batch) bool { return !b.FinishedAt.IsZero() })
}

func waitFor(t *testing.T, store catalog.BatchStore, id string, ok func(catalog.Batch) bool) catalog.Batch {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if got := batchState(t, store, id); ok(got) {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("batch %s: %+v", id, batchState(t, store, id))
		}
	}
}

func waitStopped(t *testing.T, r *Runner, id string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); r.Running(id); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the run does not stop")
		}
	}
}

// A batch whose process stopped is taken over by another once its heartbeat
// is stale: what was not done is done there, and the batch ends.
func TestBatchIsTakenOver(t *testing.T) {
	t.Parallel()
	store := catalog.NewMemoryStore()
	first := New(store, quick)
	b := start(t, first, blocked, "a", "b", "c")
	time.Sleep(50 * time.Millisecond)
	first.Close() // the process stops while working
	second := New(store, quick)
	t.Cleanup(second.Close)
	ownedBySecond := func(owner string) bool { return strings.HasPrefix(owner, second.Owner()+".") }
	if second.TakeOver(context.Background(), resolving(succeeds)); ownedBySecond(batchState(t, store, b.ID).Owner) {
		t.Fatal("a batch with a fresh heartbeat was taken over")
	}
	second.opts.Now = func() time.Time { return time.Now().Add(time.Minute) }
	second.TakeOver(context.Background(), resolving(succeeds))
	got := waitFinished(t, store, b.ID)
	for _, item := range got.Items {
		if item.State != catalog.ItemDone {
			t.Fatalf("items = %+v", got.Items)
		}
	}
	if !ownedBySecond(got.Owner) {
		t.Fatalf("owner = %s", got.Owner)
	}
}

// A run that lost its batch (paused past its heartbeat while another process
// took it over) writes nothing when it resumes, and stops.
func TestLateWriteOfALostRun(t *testing.T) {
	t.Parallel()
	store := catalog.NewMemoryStore()
	r := New(store, quick)
	t.Cleanup(r.Close)
	now := time.Now()
	b := catalog.Batch{ID: "b1", Datasource: "shop", Owner: r.run(), StartedAt: now, Heartbeat: now,
		Items: []catalog.BatchItem{{Schema: "a", State: catalog.ItemRunning}}}
	if _, _, err := store.CreateBatch(context.Background(), b, 1); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Minute)
	if ok, _ := store.ClaimBatch(context.Background(), "b1", "other.run", later, later); !ok {
		t.Fatal("not taken over")
	}
	stopped := false
	late := &batchRun{id: "b1", owner: b.Owner, stop: func() { stopped = true }}
	if r.move(late, catalog.BatchItem{Schema: "a", State: catalog.ItemDone}, catalog.ItemRunning) {
		t.Fatal("the lost run moved an item")
	}
	got := batchState(t, store, "b1")
	if got.Owner != "other.run" || got.Items[0].State != catalog.ItemRunning || !late.lost.Load() || !stopped {
		t.Fatalf("after the lost run's write: %+v (lost %v, stopped %v)", got, late.lost.Load(), stopped)
	}
}

// An item that fails is tried again after a backoff, up to Attempts times;
// one that keeps failing ends failed with why.
func TestRetries(t *testing.T) {
	t.Parallel()
	store := catalog.NewMemoryStore()
	r := New(store, quick)
	t.Cleanup(r.Close)
	var calls sync.Map
	flaky := func(_ context.Context, _, schema string) (string, string) {
		n, _ := calls.LoadOrStore(schema, new(atomic.Int32))
		if c := n.(*atomic.Int32).Add(1); schema == "broken" || c == 1 {
			return catalog.ItemFailed, fmt.Sprintf("run %d of %s failed", c, schema)
		}
		return catalog.ItemDone, ""
	}
	got := waitFinished(t, store, start(t, r, flaky, "broken", "flaky").ID)
	if broken := got.Items[0]; broken.State != catalog.ItemFailed || broken.Attempts != 3 ||
		broken.Error != "run 3 of broken failed" {
		t.Fatalf("broken = %+v", broken)
	}
	if flaky := got.Items[1]; flaky.State != catalog.ItemDone || flaky.Attempts != 2 || flaky.Error != "" {
		t.Fatalf("flaky = %+v", flaky)
	}
}

// A request sent again with its key gets the batch it started, which runs
// once.
func TestRequestKey(t *testing.T) {
	t.Parallel()
	store := catalog.NewMemoryStore()
	r := New(store, quick)
	t.Cleanup(r.Close)
	var runs atomic.Int32
	counted := func(context.Context, string, string) (string, string) {
		runs.Add(1)
		return catalog.ItemDone, ""
	}
	keyed := catalog.Batch{Datasource: "shop", RequestKey: "k1", Items: items("a")}
	first, created, err := r.Start(context.Background(), keyed, 4, counted)
	if err != nil || !created {
		t.Fatal(err, created)
	}
	again, created, err := r.Start(context.Background(), keyed, 4, counted)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("the request again = %+v, %v, created %v", again, err, created)
	}
	waitFinished(t, store, first.ID)
	if runs.Load() != 1 {
		t.Fatalf("worked %d times", runs.Load())
	}
}

// Stop stops a batch this process runs and settles what was left.
func TestStop(t *testing.T) {
	t.Parallel()
	store := catalog.NewMemoryStore()
	r := New(store, quick)
	t.Cleanup(r.Close)
	b := start(t, r, blocked, "a", "b", "c")
	time.Sleep(20 * time.Millisecond)
	r.Stop(b.ID)
	got := waitFinished(t, store, b.ID)
	for _, it := range got.Items {
		if it.State != catalog.ItemCanceled {
			t.Fatalf("items = %+v", got.Items)
		}
	}
}

// failingMoves fails the item moves of a store while fail says so.
type failingMoves struct {
	catalog.BatchStore
	fail func() bool
}

func (f failingMoves) MoveItem(
	ctx context.Context, id, owner string, now time.Time, it catalog.BatchItem, from ...string,
) (bool, error) {
	if f.fail() {
		return false, errors.New("store unavailable")
	}
	return f.BatchStore.MoveItem(ctx, id, owner, now, it, from...)
}

// A store that fails a moment does not lose work: the write is tried again
// and the batch goes on. One that keeps failing stops the run, leaving the
// batch unfinished, as last recorded, for a takeover; nothing is canceled.
func TestStoreFailures(t *testing.T) {
	t.Parallel()
	var failures atomic.Int32
	failures.Store(1)
	once := failingMoves{BatchStore: catalog.NewMemoryStore(), fail: func() bool { return failures.Add(-1) >= 0 }}
	r := New(once, quick)
	t.Cleanup(r.Close)
	if got := waitFinished(t, once, start(t, r, succeeds, "a").ID); got.Items[0].State != catalog.ItemDone {
		t.Fatalf("after a failure for a moment: %+v", got.Items)
	}
	broken := failingMoves{BatchStore: catalog.NewMemoryStore(), fail: func() bool { return true }}
	stuck := New(broken, quick)
	t.Cleanup(stuck.Close)
	b := start(t, stuck, succeeds, "a")
	waitStopped(t, stuck, b.ID)
	if got := batchState(t, broken, b.ID); !got.FinishedAt.IsZero() || got.Items[0].State != catalog.ItemPending {
		t.Fatalf("a run the store failed ended the batch: %+v", got)
	}
}

// lostAnswers makes the item moves of a store and then, while lose says so,
// answers with an error as if the connection broke after the commit.
type lostAnswers struct {
	catalog.BatchStore
	lose func() bool
}

func (l lostAnswers) MoveItem(
	ctx context.Context, id, owner string, now time.Time, it catalog.BatchItem, from ...string,
) (bool, error) {
	moved, err := l.BatchStore.MoveItem(ctx, id, owner, now, it, from...)
	if err == nil && l.lose() {
		return false, errors.New("connection reset")
	}
	return moved, err
}

// A move made whose answer was lost counts as made: the item is worked, not
// skipped.
func TestLostAnswers(t *testing.T) {
	t.Parallel()
	var lose atomic.Int32
	lose.Store(1)
	store := lostAnswers{BatchStore: catalog.NewMemoryStore(), lose: func() bool { return lose.Add(-1) >= 0 }}
	r := New(store, quick)
	t.Cleanup(r.Close)
	var runs atomic.Int32
	counted := func(context.Context, string, string) (string, string) {
		runs.Add(1)
		return catalog.ItemDone, ""
	}
	got := waitFinished(t, store, start(t, r, counted, "a").ID)
	if got.Items[0].State != catalog.ItemDone || got.Items[0].Attempts != 1 || runs.Load() != 1 {
		t.Fatalf("after a lost answer: %+v, worked %d times", got.Items, runs.Load())
	}
}

// A takeover that cannot queue the items being worked again does not run
// the batch: it stays unfinished, as it was, and a later takeover runs it.
func TestTakeoverWaitsForTheStore(t *testing.T) {
	t.Parallel()
	var down atomic.Bool
	down.Store(true)
	store := failingMoves{BatchStore: catalog.NewMemoryStore(), fail: down.Load}
	old := time.Now().Add(-time.Hour)
	if _, _, err := store.CreateBatch(context.Background(), catalog.Batch{ID: "b1", Datasource: "shop",
		Owner: "gone.run", StartedAt: old, Heartbeat: old, Items: []catalog.BatchItem{
			{Schema: "a", State: catalog.ItemRunning, Attempts: 1}, {Schema: "b", State: catalog.ItemPending}}}, 1); err != nil {
		t.Fatal(err)
	}
	r := New(store, quick)
	t.Cleanup(r.Close)
	r.TakeOver(context.Background(), resolving(succeeds))
	if got := batchState(t, store, "b1"); !got.FinishedAt.IsZero() || got.Items[0].State != catalog.ItemRunning {
		t.Fatalf("a takeover the store failed ran the batch: %+v", got)
	}
	down.Store(false)
	r.opts.Now = func() time.Time { return time.Now().Add(time.Minute) }
	r.TakeOver(context.Background(), resolving(succeeds))
	got := waitFinished(t, store, "b1")
	for _, it := range got.Items {
		if it.State != catalog.ItemDone {
			t.Fatalf("after the store is back: %+v", got.Items)
		}
	}
}

// Closing the bus stops the running batches as the process stopping does:
// Close does not wait for them, and they stay unfinished for a takeover.
func TestBusCloseStopsBatches(t *testing.T) {
	t.Parallel()
	store := catalog.NewMemoryStore()
	b := bus.New(8, nil)
	opts := quick
	opts.Bus = b
	r := New(store, opts)
	t.Cleanup(r.Close)
	r.Watch(resolving(succeeds))
	batch := start(t, r, blocked, "a", "b")
	time.Sleep(50 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := b.Close(ctx); err != nil {
		t.Fatalf("bus close = %v; tasks %+v", err, b.Stats().Tasks)
	}
	if got := batchState(t, store, batch.ID); !got.FinishedAt.IsZero() {
		t.Fatalf("closing the bus finished the batch: %+v", got)
	}
}

// hangingMoves never answers a move until its context ends.
type hangingMoves struct {
	catalog.BatchStore
	entered chan struct{}
}

func (h hangingMoves) MoveItem(ctx context.Context, _, _ string, _ time.Time, _ catalog.BatchItem, _ ...string,
) (bool, error) {
	select {
	case h.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return false, ctx.Err()
}

// A store that hangs does not hang closing: the writes of progress end with
// the process.
func TestCloseDoesNotWaitForAHangingStore(t *testing.T) {
	t.Parallel()
	store := hangingMoves{BatchStore: catalog.NewMemoryStore(), entered: make(chan struct{}, 1)}
	r := New(store, quick)
	start(t, r, succeeds, "a")
	<-store.entered
	closed := make(chan struct{})
	go func() { r.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("closing waited for a store that hangs")
	}
}
