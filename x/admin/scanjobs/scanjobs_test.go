package scanjobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/bus"
)

func waitState[T any](t *testing.T, p *Pool[T], id string, want State) Job[T] {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		j, ok := p.Get(id)
		if ok && j.State == want {
			return j
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s = %+v (found %v), want %s", id, j, ok, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPoolRunsJobsOffTheCaller(t *testing.T) {
	t.Parallel()
	p := New[int](Options{Workers: 1})
	defer p.Close()
	release := make(chan struct{})
	slow, err := p.Start("a", func(ctx context.Context) (int, error) {
		<-release
		return 1, nil
	})
	if err != nil || slow.State != Pending {
		t.Fatalf("Start = %+v, %v; it must return before the job runs", slow, err)
	}
	failing, _ := p.Start("b", func(context.Context) (int, error) { return 0, errors.New("boom") })
	waitState(t, p, slow.ID, Running)
	if j, _ := p.Get(failing.ID); j.State != Pending {
		t.Fatalf("with one worker the second job waits: %+v", j)
	}
	close(release)
	if j := waitState(t, p, slow.ID, Done); j.Result != 1 {
		t.Fatalf("result = %+v", j)
	}
	if j := waitState(t, p, failing.ID, Failed); j.Err != "boom" {
		t.Fatalf("failure = %+v", j)
	}
}

func TestPoolCancelsAndTimesOut(t *testing.T) {
	t.Parallel()
	p := New[int](Options{Workers: 1, Timeout: 50 * time.Millisecond})
	defer p.Close()
	block := func(ctx context.Context) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	running, _ := p.Start("a", block)
	queued, _ := p.Start("b", block)
	waitState(t, p, running.ID, Running)
	if j, ok := p.Cancel(queued.ID); !ok || j.State != Canceled {
		t.Fatalf("cancel pending = %+v, %v", j, ok)
	}
	if j := waitState(t, p, running.ID, Failed); j.Err != context.DeadlineExceeded.Error() {
		t.Fatalf("timeout = %+v", j)
	}
	again, _ := p.Start("c", block)
	waitState(t, p, again.ID, Running)
	p.Cancel(again.ID)
	waitState(t, p, again.ID, Canceled)
}

func TestPoolRejectsWhenTheQueueIsFull(t *testing.T) {
	t.Parallel()
	p := New[int](Options{Workers: 1, Queue: 1})
	defer p.Close()
	block := func(ctx context.Context) (int, error) { <-ctx.Done(); return 0, ctx.Err() }
	first, _ := p.Start("a", block)
	waitState(t, p, first.ID, Running)
	if _, err := p.Start("b", block); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Start("c", block); !errors.Is(err, ErrBusy) {
		t.Fatalf("a full queue must refuse: %v", err)
	}
}

func TestPoolForgetsFinishedJobs(t *testing.T) {
	t.Parallel()
	p := New[int](Options{Keep: 10 * time.Millisecond})
	defer p.Close()
	j, _ := p.Start("a", func(context.Context) (int, error) { return 1, nil })
	waitState(t, p, j.ID, Done)
	time.Sleep(20 * time.Millisecond)
	if _, ok := p.Get(j.ID); ok {
		t.Fatal("a job finished longer ago than Keep must expire")
	}
}

// A job that panics fails; the worker and the process carry on.
func TestPoolSurvivesAPanickingJob(t *testing.T) {
	t.Parallel()
	p := New[int](Options{Workers: 1})
	defer p.Close()
	bad, _ := p.Start("bad", func(context.Context) (int, error) { panic("driver bug") })
	if j := waitState(t, p, bad.ID, Failed); j.Err != "internal error: driver bug" {
		t.Fatalf("panicking job = %+v", j)
	}
	good, _ := p.Start("good", func(context.Context) (int, error) { return 7, nil })
	if j := waitState(t, p, good.ID, Done); j.Result != 7 {
		t.Fatalf("the next job on the same worker = %+v", j)
	}
}

// On a bus the Scan lane bounds the jobs: its concurrency runs them and its
// queue holds the rest; past both, Start is busy.
func TestPoolRunsOnTheScanLane(t *testing.T) {
	t.Parallel()
	b := bus.New(8, map[bus.LaneName]bus.Limits{bus.Scan: {MaxConcurrent: 1, MaxQueue: 1}})
	defer func() { _ = b.Close(context.Background()) }()
	p := New[int](Options{Bus: b})
	defer p.Close()
	block := func(ctx context.Context) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	running, _ := p.Start("a", block)
	if _, err := p.Start("b", block); err != nil {
		t.Fatal(err)
	}
	waitState(t, p, running.ID, Running)
	if _, err := p.Start("c", block); !errors.Is(err, ErrBusy) {
		t.Fatalf("third job = %v, want ErrBusy", err)
	}
	for _, lane := range b.Stats().Lanes {
		if lane.Name == bus.Scan && lane.Inflight != 1 {
			t.Fatalf("scan lane %+v", lane)
		}
	}
}

// Closing the bus stops the pool: its dispatcher and running jobs end, so
// Close does not wait for them.
func TestBusCloseStopsThePool(t *testing.T) {
	t.Parallel()
	b := bus.New(8, nil)
	p := New[int](Options{Bus: b})
	running, _ := p.Start("a", func(ctx context.Context) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	})
	waitState(t, p, running.ID, Running)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := b.Close(ctx); err != nil {
		t.Fatalf("bus close = %v; tasks %+v", err, b.Stats().Tasks)
	}
	p.Close()
}

// The Scan lane counts every job waiting, not only the next one.
func TestScanLaneCountsPendingJobs(t *testing.T) {
	t.Parallel()
	b := bus.New(8, map[bus.LaneName]bus.Limits{bus.Scan: {MaxConcurrent: 1, MaxQueue: 4}})
	defer func() { _ = b.Close(context.Background()) }()
	p := New[int](Options{Bus: b})
	defer p.Close()
	block := func(ctx context.Context) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	running, _ := p.Start("a", block)
	waitState(t, p, running.ID, Running)
	var queued []Job[int]
	for range 3 {
		j, err := p.Start("b", block)
		if err != nil {
			t.Fatal(err)
		}
		queued = append(queued, j)
	}
	scan := func() bus.LaneStats { return b.Stats().Lanes[2] }
	if st := scan(); st.Inflight != 1 || st.Queued != 3 {
		t.Fatalf("scan lane %+v, want 1 running and 3 waiting", st)
	}
	p.Cancel(queued[0].ID)
	if st := scan(); st.Queued != 2 {
		t.Fatalf("a canceled job still counts as waiting: %+v", st)
	}
}

// Closing the bus stops the pool while a job waits for the Scan lane.
func TestBusCloseStopsThePoolWithQueuedJobs(t *testing.T) {
	t.Parallel()
	b := bus.New(8, map[bus.LaneName]bus.Limits{bus.Scan: {MaxConcurrent: 1, MaxQueue: 1}})
	p := New[int](Options{Bus: b})
	block := func(ctx context.Context) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	running, _ := p.Start("a", block)
	waitState(t, p, running.ID, Running)
	if _, err := p.Start("b", block); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := b.Close(ctx); err != nil {
		t.Fatalf("bus close = %v; tasks %+v", err, b.Stats().Tasks)
	}
	p.Close()
}

// A job holding the Scan lane while it waits for IO leaves no room: the pool
// takes no more jobs than the lane's concurrency and queue.
func TestPoolCountsJobsWaitingForIO(t *testing.T) {
	t.Parallel()
	b := bus.New(2, map[bus.LaneName]bus.Limits{
		bus.Admin: {MaxConcurrent: 1}, bus.Scan: {MaxConcurrent: 1, MaxQueue: 1},
	})
	defer func() { _ = b.Close(context.Background()) }()
	_, releaseAdmin, err := b.Lane(bus.Admin).Acquire(context.Background()) // holds the auxiliary IO
	if err != nil {
		t.Fatal(err)
	}
	defer releaseAdmin()
	p := New[int](Options{Bus: b})
	defer p.Close()
	nothing := func(context.Context) (int, error) { return 0, nil }
	for _, label := range []string{"a", "b"} {
		if _, err := p.Start(label, nothing); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.Start("c", nothing); !errors.Is(err, ErrBusy) {
		t.Fatalf("third job = %v, want ErrBusy", err)
	}
}
