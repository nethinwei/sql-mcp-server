package engine

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/ratelimit"
)

func TestSubmitExecutes(t *testing.T) {
	t.Parallel()
	e, _ := New(WithIOPool(2), WithMaxInflight(4))
	ran := false
	if _, err := e.Submit(context.Background(), "k", func(_ context.Context) (any, error) {
		ran = true
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Fatal("fn did not run")
	}
}

func TestSubmitOverloaded(t *testing.T) {
	t.Parallel()
	e, _ := New(WithIOPool(1), WithMaxInflight(1))
	block := make(chan struct{})
	go func() {
		_, _ = e.Submit(context.Background(), "k1", func(_ context.Context) (any, error) {
			<-block
			return nil, nil
		})
	}()
	time.Sleep(20 * time.Millisecond)
	_, err := e.Submit(context.Background(), "k2", func(_ context.Context) (any, error) { return nil, nil })
	if !errors.Is(err, ErrOverloaded) {
		t.Fatalf("got %v, want ErrOverloaded", err)
	}
	close(block)
	time.Sleep(20 * time.Millisecond)
}

func TestSubmitSingleflightDedup(t *testing.T) {
	t.Parallel()
	e, _ := New(WithIOPool(4), WithMaxInflight(10))
	var calls atomic.Int64
	var wg sync.WaitGroup
	key := "same-query"
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = e.Submit(context.Background(), key, func(_ context.Context) (any, error) {
				calls.Add(1)
				time.Sleep(20 * time.Millisecond)
				return nil, nil
			})
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("fn called %d times, want 1", calls.Load())
	}
}

func TestSubmitPanicRecovered(t *testing.T) {
	t.Parallel()
	e, _ := New(WithIOPool(1), WithMaxInflight(2))
	_, err := e.Submit(context.Background(), "k", func(_ context.Context) (any, error) {
		panic("boom")
	})
	if err == nil || !contains(err.Error(), "panic recovered") {
		t.Fatalf("got %v, want panic-recovered error", err)
	}
}

func TestSubmitBreakerOpen(t *testing.T) {
	t.Parallel()
	b := ratelimit.NewBreaker(1, time.Second)
	e, _ := New(WithIOPool(1), WithMaxInflight(2), WithBreaker(b))
	_, _ = e.Submit(context.Background(), "k", func(_ context.Context) (any, error) {
		return nil, errors.New("fail")
	})
	_, err := e.Submit(context.Background(), "k2", func(_ context.Context) (any, error) { return nil, nil })
	if !errors.Is(err, ratelimit.ErrCircuitOpen) {
		t.Fatalf("got %v, want ErrCircuitOpen", err)
	}
}

func TestSubmitIgnoredFailureDoesNotAffectBreakerOrAIMD(t *testing.T) {
	t.Parallel()
	businessErr := errors.New("business rejection")
	breaker := ratelimit.NewBreaker(1, time.Second)
	limiter := ratelimit.NewAdaptive(4, 1, 8, 0)
	e, _ := New(
		WithIOPool(4), WithMaxInflight(8), WithBreaker(breaker), WithLimiter(limiter),
		WithFailureClassifier(func(err error) bool { return !errors.Is(err, businessErr) }),
	)
	before := limiter.Limit()
	_, err := e.Submit(context.Background(), "", func(context.Context) (any, error) {
		return nil, businessErr
	})
	if !errors.Is(err, businessErr) {
		t.Fatal(err)
	}
	if breaker.IsOpen() || limiter.Limit() != before {
		t.Fatalf("business error changed health controls: breaker=%v limit=%d", breaker.IsOpen(), limiter.Limit())
	}
	systemErr := errors.New("provider failed")
	_, _ = e.Submit(context.Background(), "", func(context.Context) (any, error) {
		return nil, systemErr
	})
	if !breaker.IsOpen() || limiter.Limit() >= before {
		t.Fatalf("system error did not change health controls: breaker=%v limit=%d", breaker.IsOpen(), limiter.Limit())
	}
}

func TestSubmitCtxCancel(t *testing.T) {
	t.Parallel()
	e, _ := New(WithIOPool(1), WithMaxInflight(2))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// occupy the io slot so the next Submit blocks on iosem and observes ctx
	block := make(chan struct{})
	go func() {
		_, _ = e.Submit(context.Background(), "occupier", func(_ context.Context) (any, error) {
			<-block
			return nil, nil
		})
	}()
	time.Sleep(20 * time.Millisecond)
	_, err := e.Submit(ctx, "k", func(_ context.Context) (any, error) { return nil, nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	close(block)
}

func TestNewInvalidConfig(t *testing.T) {
	t.Parallel()
	if _, err := New(WithIOPool(0)); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("got %v, want ErrInvalidConfig", err)
	}
}

func TestSubmitEmptyKeyNoDedup(t *testing.T) {
	t.Parallel()
	e, _ := New(WithIOPool(4), WithMaxInflight(10))
	var calls atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = e.Submit(context.Background(), "", func(_ context.Context) (any, error) {
				calls.Add(1)
				time.Sleep(10 * time.Millisecond)
				return nil, nil
			})
		}()
	}
	wg.Wait()
	if calls.Load() != 5 {
		t.Fatalf("empty key must not dedup: called %d, want 5", calls.Load())
	}
}

func TestSubmitAdaptiveLimitRejects(t *testing.T) {
	t.Parallel()
	// limit fixed at 1: a second concurrent submit is rejected by AIMD Acquire.
	lim := ratelimit.NewAdaptive(1, 1, 1, 0)
	e, _ := New(WithIOPool(4), WithMaxInflight(10), WithLimiter(lim))
	started := make(chan struct{})
	block := make(chan struct{})
	go func() {
		_, _ = e.Submit(context.Background(), "", func(_ context.Context) (any, error) {
			close(started)
			<-block
			return nil, nil
		})
	}()
	<-started
	_, err := e.Submit(context.Background(), "", func(_ context.Context) (any, error) { return nil, nil })
	if !errors.Is(err, ratelimit.ErrRateLimited) {
		t.Fatalf("got %v, want ErrRateLimited", err)
	}
	close(block)
}

func TestSubmitRPSLimitRejects(t *testing.T) {
	t.Parallel()
	e, _ := New(WithRPSLimiter(ratelimit.NewTokenBucket(1)))
	if _, err := e.Submit(context.Background(), "", func(context.Context) (any, error) {
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Submit(context.Background(), "", func(context.Context) (any, error) {
		return nil, nil
	}); !errors.Is(err, ratelimit.ErrRateLimited) {
		t.Fatalf("second submit error = %v", err)
	}
}

func TestSubmitSingleflightSharesResult(t *testing.T) {
	t.Parallel()
	e, _ := New(WithIOPool(4), WithMaxInflight(10))
	var wg sync.WaitGroup
	results := make([]any, 3)
	start := make(chan struct{})
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			v, _ := e.Submit(context.Background(), "shared", func(_ context.Context) (any, error) {
				time.Sleep(20 * time.Millisecond)
				return 42, nil
			})
			results[idx] = v
		}(i)
	}
	close(start)
	wg.Wait()
	for i, v := range results {
		if v != 42 {
			t.Fatalf("waiter %d got %v, want shared result 42", i, v)
		}
	}
}

func TestSingleflightWaiterContextCancellation(t *testing.T) {
	t.Parallel()
	e, _ := New(WithIOPool(2), WithMaxInflight(4))
	started := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_, _ = e.Submit(context.Background(), "shared", func(context.Context) (any, error) {
			close(started)
			<-release
			return 1, nil
		})
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := e.Submit(ctx, "shared", func(context.Context) (any, error) {
		t.Fatal("waiter must not execute")
		return nil, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want context deadline", err)
	}
	close(release)
}

func TestSingleflightLeaderCancellationDoesNotCancelRemainingWaiter(t *testing.T) {
	t.Parallel()
	e, _ := New(WithIOPool(2), WithMaxInflight(4))
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	leaderDone := make(chan error, 1)
	go func() {
		_, err := e.Submit(leaderCtx, "shared-cancel", func(ctx context.Context) (any, error) {
			close(started)
			select {
			case <-release:
				return 7, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		})
		leaderDone <- err
	}()
	<-started
	waiterDone := make(chan struct {
		value any
		err   error
	}, 1)
	go func() {
		value, err := e.Submit(context.Background(), "shared-cancel", func(context.Context) (any, error) {
			t.Error("waiter must not execute")
			return nil, nil
		})
		waiterDone <- struct {
			value any
			err   error
		}{value, err}
	}()
	deadline := time.Now().Add(time.Second)
	for {
		e.sf.mu.Lock()
		call := e.sf.m["0\x00shared-cancel"]
		joined := call != nil && call.waiters == 2
		e.sf.mu.Unlock()
		if joined {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("waiter did not join shared execution")
		}
		time.Sleep(time.Millisecond)
	}
	cancelLeader()
	if err := <-leaderDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader error = %v", err)
	}
	close(release)
	got := <-waiterDone
	if got.err != nil || got.value != 7 {
		t.Fatalf("waiter = %v, %v; want 7, nil", got.value, got.err)
	}
}

func TestSingleflightPropagatesLeaderDeadline(t *testing.T) {
	t.Parallel()
	e, _ := New(WithIOPool(1), WithMaxInflight(2))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := e.Submit(ctx, "deadline", func(runCtx context.Context) (any, error) {
		deadline, ok := runCtx.Deadline()
		if !ok {
			t.Fatal("shared execution context has no deadline")
		}
		if want, _ := ctx.Deadline(); !deadline.Equal(want) {
			t.Fatalf("execution deadline = %v, want %v", deadline, want)
		}
		<-runCtx.Done()
		return nil, runCtx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want context deadline", err)
	}
	drainCtx, drainCancel := context.WithTimeout(context.Background(), time.Second)
	defer drainCancel()
	if err := e.Drain(drainCtx); err != nil {
		t.Fatalf("drain after deadline: %v", err)
	}
}

func TestSingleflightCancelsExecutionAfterAllWaitersCancel(t *testing.T) {
	t.Parallel()
	e, _ := New(WithIOPool(1), WithMaxInflight(4))
	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	started := make(chan struct{})
	executionCanceled := make(chan error, 1)
	results := make(chan error, 2)
	go func() {
		_, err := e.Submit(ctx1, "all-cancel", func(ctx context.Context) (any, error) {
			close(started)
			<-ctx.Done()
			executionCanceled <- ctx.Err()
			return nil, ctx.Err()
		})
		results <- err
	}()
	<-started
	go func() {
		_, err := e.Submit(ctx2, "all-cancel", func(context.Context) (any, error) {
			t.Error("waiter must not execute")
			return nil, nil
		})
		results <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		e.sf.mu.Lock()
		call := e.sf.m["0\x00all-cancel"]
		joined := call != nil && call.waiters == 2
		e.sf.mu.Unlock()
		if joined {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second waiter did not join")
		}
		time.Sleep(time.Millisecond)
	}
	cancel1()
	select {
	case err := <-executionCanceled:
		t.Fatalf("one canceled waiter stopped shared execution: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	cancel2()
	select {
	case err := <-executionCanceled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("execution error = %v, want canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shared execution was not canceled after all waiters left")
	}
	for range 2 {
		if err := <-results; !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter error = %v, want canceled", err)
		}
	}
}

func TestDrainRejectsAndWaits(t *testing.T) {
	t.Parallel()
	e, _ := New(WithIOPool(1), WithMaxInflight(4))
	started := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_, _ = e.Submit(context.Background(), "", func(context.Context) (any, error) {
			close(started)
			<-release
			return nil, nil
		})
	}()
	<-started
	drained := make(chan error, 1)
	go func() { drained <- e.Drain(context.Background()) }()

	deadline := time.Now().Add(time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		_, err := e.Submit(ctx, "", func(context.Context) (any, error) { return nil, nil })
		cancel()
		if errors.Is(err, ErrClosed) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("engine did not close admission, last error %v", err)
		}
	}
	select {
	case err := <-drained:
		t.Fatalf("drain returned before in-flight work finished: %v", err)
	default:
	}
	close(release)
	if err := <-drained; err != nil {
		t.Fatal(err)
	}
}

func TestDrainWaitsForSingleflightExecutionAfterAllWaitersCancel(t *testing.T) {
	e, _ := New(WithIOPool(1), WithMaxInflight(4))
	started := make(chan struct{})
	release := make(chan struct{})
	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	results := make(chan error, 2)
	run := func(ctx context.Context) {
		_, err := e.Submit(ctx, "detached", func(context.Context) (any, error) {
			close(started)
			<-release // deliberately ignores cancellation
			return nil, nil
		})
		results <- err
	}
	go run(ctx1)
	<-started
	go run(ctx2)
	deadline := time.Now().Add(time.Second)
	for {
		e.sf.mu.Lock()
		call := e.sf.m["0\x00detached"]
		joined := call != nil && call.waiters == 2
		e.sf.mu.Unlock()
		if joined {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second waiter did not join")
		}
		time.Sleep(time.Millisecond)
	}
	cancel1()
	cancel2()
	for i := 0; i < 2; i++ {
		if err := <-results; !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter error = %v, want canceled", err)
		}
	}
	drained := make(chan error, 1)
	go func() { drained <- e.Drain(context.Background()) }()
	select {
	case err := <-drained:
		t.Fatalf("Drain returned while execution goroutine was running: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-drained; err != nil {
		t.Fatal(err)
	}
}

func TestSubmitCPUBoundsConcurrency(t *testing.T) {
	t.Parallel()
	e, _ := New(WithIOPool(1), WithCPUPool(1), WithMaxInflight(4))
	started := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_, _ = e.SubmitCPU(context.Background(), "", func(context.Context) (any, error) {
			close(started)
			<-release
			return nil, nil
		})
	}()
	<-started
	ioCtx, ioCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer ioCancel()
	if _, err := e.Submit(ioCtx, "", func(context.Context) (any, error) { return nil, nil }); err != nil {
		t.Fatalf("IO work should use a pool independent from CPU: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := e.SubmitCPU(ctx, "", func(context.Context) (any, error) {
		t.Fatal("second CPU task must not pass a full CPU pool")
		return nil, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want context deadline", err)
	}
	close(release)
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
