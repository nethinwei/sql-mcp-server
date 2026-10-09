package scanjobs

import (
	"context"
	"errors"
	"testing"
	"time"
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
