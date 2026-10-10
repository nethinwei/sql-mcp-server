package bus

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// hold admits work on lane and keeps it running until the returned func is
// called.
func hold(t *testing.T, lane *Lane) func() {
	t.Helper()
	admitted, done := make(chan struct{}), make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_ = lane.Run(context.Background(), func(context.Context) error {
			close(admitted)
			<-done
			return nil
		})
	}()
	<-admitted
	return func() { close(done); <-finished }
}

func TestLaneQueueFullRejects(t *testing.T) {
	t.Parallel()
	b := New(8, map[LaneName]Limits{Admin: {MaxConcurrent: 1, MaxQueue: 1}})
	release := hold(t, b.Lane(Admin))
	waited := make(chan error, 1)
	go func() {
		waited <- b.Lane(Admin).Run(context.Background(), func(context.Context) error { return nil })
	}()
	waitFor(t, func() bool { return b.Lane(Admin).Stats().Queued == 1 })
	err := b.Lane(Admin).Run(context.Background(), func(context.Context) error { return nil })
	if !errors.Is(err, ErrOverloaded) {
		t.Fatalf("third call = %v, want ErrOverloaded", err)
	}
	release()
	if err := <-waited; err != nil {
		t.Fatalf("queued call = %v", err)
	}
	st := b.Lane(Admin).Stats()
	if st.Rejected != 1 || st.Completed != 2 || st.Inflight != 0 || st.Queued != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

// The lanes other than Serve hold at most half of the IO slots together.
func TestAuxiliaryShareLeavesIOForServe(t *testing.T) {
	t.Parallel()
	b := New(2, map[LaneName]Limits{Admin: {MaxConcurrent: 4, MaxQueue: 4}})
	release := hold(t, b.Lane(Admin))
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := b.Lane(Scan).Run(ctx, func(context.Context) error { return nil }); !errors.Is(err,
		context.DeadlineExceeded) {
		t.Fatalf("scan ran past the auxiliary share: %v", err)
	}
	if !b.IO().TryAcquire() {
		t.Fatal("serve must still find an IO slot")
	}
	b.IO().Release()
}

func TestUpdateResizesLanes(t *testing.T) {
	t.Parallel()
	b := New(8, map[LaneName]Limits{Scan: {MaxConcurrent: 1}})
	release := hold(t, b.Lane(Scan))
	defer release()
	b.Update(8, map[LaneName]Limits{Scan: {MaxConcurrent: 2}})
	if err := b.Lane(Scan).Run(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Fatalf("growing the lane must admit more work: %v", err)
	}
	if got := b.Lane(Scan).Stats().MaxConcurrent; got != 2 {
		t.Fatalf("MaxConcurrent = %d", got)
	}
}

// Nested work runs under its caller's admission instead of taking a slot.
func TestEnsureReusesAdmission(t *testing.T) {
	t.Parallel()
	b := New(8, map[LaneName]Limits{Scan: {MaxConcurrent: 1}, Admin: {MaxConcurrent: 1}})
	err := b.Lane(Scan).Run(context.Background(), func(ctx context.Context) error {
		return b.Lane(Admin).Ensure(ctx, func(ctx context.Context) error {
			if name, _ := Admitted(ctx); name != Scan {
				t.Errorf("admitted on %q", name)
			}
			if st := b.Lane(Admin).Stats(); st.Inflight != 0 {
				t.Errorf("admin took a slot: %+v", st)
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestServeTryEnter(t *testing.T) {
	t.Parallel()
	b := New(8, map[LaneName]Limits{Serve: {MaxConcurrent: 1}})
	release, err := b.Lane(Serve).TryEnter()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Lane(Serve).TryEnter(); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("second = %v", err)
	}
	release()
	if b.IO().Stats().Used != 0 {
		t.Fatal("TryEnter must not take IO")
	}
}

func TestCloseCancelsAndWaitsForTasks(t *testing.T) {
	t.Parallel()
	b := New(0, nil)
	var stopped atomic.Bool
	started := make(chan struct{})
	b.Go("loop", func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		stopped.Store(true)
	})
	<-started
	if st := b.Stats(); len(st.Tasks) != 1 || st.Tasks[0].Running != 1 {
		t.Fatalf("tasks = %+v", st.Tasks)
	}
	if err := b.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !stopped.Load() {
		t.Fatal("Close returned before the task")
	}
	if b.Go("late", func(context.Context) {}) {
		t.Fatal("a closed bus started a task")
	}
	if err := b.Lane(Admin).Run(context.Background(), func(context.Context) error { return nil }); !errors.Is(err,
		ErrClosed) {
		t.Fatalf("closed lane = %v", err)
	}
}

func TestTaskPanicIsRecovered(t *testing.T) {
	t.Parallel()
	b := New(0, nil)
	b.Go("bad", func(context.Context) { panic("boom") })
	if err := b.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(b.Stats().Tasks) != 0 {
		t.Fatal("a panicked task stays counted")
	}
}

func TestAfterFunc(t *testing.T) {
	t.Parallel()
	b := New(0, nil)
	ran := make(chan struct{})
	b.AfterFunc("timer", time.Millisecond, func() { close(ran) })
	<-ran
	stop := b.AfterFunc("never", time.Hour, func() { t.Error("stopped timer ran") })
	if !stop() {
		t.Fatal("stop = false")
	}
	_ = b.Close(context.Background())
}

func TestNilBusRunsUnbounded(t *testing.T) {
	t.Parallel()
	var b *Bus
	if err := b.Lane(Admin).Run(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	b.Go("x", func(context.Context) { close(done) })
	<-done
}

func TestSemaphoreWaiterCanceled(t *testing.T) {
	t.Parallel()
	s := NewSemaphore(1)
	if err := s.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("acquire = %v", err)
	}
	s.Release()
	if st := s.Stats(); st.Used != 0 || st.Waiting != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestForkRunsOnceClosed(t *testing.T) {
	t.Parallel()
	b := New(0, nil)
	var wg sync.WaitGroup
	var ran atomic.Int32
	b.Fork(&wg, "open", func(context.Context) { ran.Add(1) })
	wg.Wait()
	_ = b.Close(context.Background())
	b.Fork(&wg, "closed", func(context.Context) { ran.Add(1) })
	wg.Wait()
	if ran.Load() != 2 {
		t.Fatalf("ran %d", ran.Load())
	}
}

// RunWaiting waits on a full queue instead of failing.
func TestRunWaitingIgnoresTheQueueLimit(t *testing.T) {
	t.Parallel()
	b := New(8, map[LaneName]Limits{Background: {MaxConcurrent: 1, MaxQueue: 0}})
	release := hold(t, b.Lane(Background))
	done := make(chan error, 1)
	go func() {
		done <- b.Lane(Background).RunWaiting(context.Background(), func(context.Context) error { return nil })
	}()
	waitFor(t, func() bool { return b.Lane(Background).Stats().Queued == 1 })
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Tickets count as waiting from Queue on, fail Queue past the lane's room,
// and admit in the order they are acquired.
func TestTickets(t *testing.T) {
	t.Parallel()
	b := New(8, map[LaneName]Limits{Scan: {MaxConcurrent: 1, MaxQueue: 2}})
	lane := b.Lane(Scan)
	var tickets []*Ticket
	for range 3 {
		ticket, err := lane.Queue()
		if err != nil {
			t.Fatal(err)
		}
		tickets = append(tickets, ticket)
	}
	if _, err := lane.Queue(); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("fourth ticket = %v", err)
	}
	if st := lane.Stats(); st.Queued != 3 {
		t.Fatalf("stats = %+v", st)
	}
	_, release, err := tickets[0].Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tickets[1].Cancel()
	if _, _, err := tickets[1].Acquire(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ticket = %v", err)
	}
	if st := lane.Stats(); st.Inflight != 1 || st.Queued != 1 {
		t.Fatalf("stats = %+v", st)
	}
	release()
	_, release, err = tickets[2].Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
	if st := lane.Stats(); st.Inflight != 0 || st.Queued != 0 || st.Completed != 2 {
		t.Fatalf("stats = %+v", st)
	}
}

// Work holding a lane slot while it waits for IO still counts: it is waiting,
// and Queue leaves no room for it.
func TestWaitingForIOCounts(t *testing.T) {
	t.Parallel()
	b := New(2, map[LaneName]Limits{Admin: {MaxConcurrent: 1, MaxQueue: 1}, Scan: {MaxConcurrent: 1, MaxQueue: 1}})
	release := hold(t, b.Lane(Admin)) // holds the only auxiliary IO slot
	defer release()
	scan := b.Lane(Scan)
	first, err := scan.Queue()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_, done, err := first.Acquire(context.Background())
		if err == nil {
			done()
		}
	}()
	waitFor(t, func() bool { return scan.slots.Stats().Used == 1 })
	if _, err := scan.Queue(); err != nil {
		t.Fatal(err)
	}
	if _, err := scan.Queue(); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("third ticket = %v, want ErrOverloaded", err)
	}
	if st := scan.Stats(); st.Queued != 2 || st.Inflight != 0 {
		t.Fatalf("stats = %+v, want 2 waiting", st)
	}
}
