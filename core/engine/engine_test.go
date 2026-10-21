package engine

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/ratelimit"
)

func TestSubmitExecutes(t *testing.T) {
	t.Parallel()
	e, _ := New(WithIOPool(2), WithMaxInflight(4))
	ran := false
	if _, err := Submit(context.Background(), e, "k", func(_ context.Context) (any, error) {
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
		_, _ = Submit(context.Background(), e, "k1", func(_ context.Context) (any, error) {
			<-block
			return nil, nil
		})
	}()
	time.Sleep(20 * time.Millisecond)
	_, err := Submit(context.Background(), e, "k2", func(_ context.Context) (any, error) { return nil, nil })
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
			_, _ = Submit(context.Background(), e, key, func(_ context.Context) (any, error) {
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
	_, err := Submit(context.Background(), e, "k", func(_ context.Context) (any, error) {
		panic("boom")
	})
	if err == nil || !contains(err.Error(), "panic recovered") {
		t.Fatalf("got %v, want panic-recovered error", err)
	}
}

// A panic past the tool's own recovery (classifying its error) fails the
// call and frees its key: the next call of the key runs.
func TestSubmitPanicOutsideTheToolFreesItsKey(t *testing.T) {
	t.Parallel()
	e, _ := New(WithIOPool(1), WithMaxInflight(2), WithFailureClassifier(func(error) bool { panic("classify") }))
	_, err := Submit(context.Background(), e, "k", func(context.Context) (any, error) { return nil, errors.New("x") })
	if err == nil || !contains(err.Error(), "panic recovered") {
		t.Fatalf("got %v, want panic-recovered error", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if v, err := Submit(ctx, e, "k", func(context.Context) (any, error) { return 1, nil }); err != nil || v != 1 {
		t.Fatalf("the key after the panic = %v, %v", v, err)
	}
}

func TestSubmitBreakerOpen(t *testing.T) {
	t.Parallel()
	b := ratelimit.NewBreaker(1, time.Second)
	e, _ := New(WithIOPool(1), WithMaxInflight(2), WithBreaker(b))
	_, _ = Submit(context.Background(), e, "k", func(_ context.Context) (any, error) {
		return nil, errors.New("fail")
	})
	_, err := Submit(context.Background(), e, "k2", func(_ context.Context) (any, error) { return nil, nil })
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
	_, err := Submit(context.Background(), e, "", func(context.Context) (any, error) {
		return nil, businessErr
	})
	if !errors.Is(err, businessErr) {
		t.Fatal(err)
	}
	if breaker.IsOpen() || limiter.Limit() != before {
		t.Fatalf("business error changed health controls: breaker=%v limit=%d", breaker.IsOpen(), limiter.Limit())
	}
	systemErr := errors.New("provider failed")
	_, _ = Submit(context.Background(), e, "", func(context.Context) (any, error) {
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
		_, _ = Submit(context.Background(), e, "occupier", func(_ context.Context) (any, error) {
			<-block
			return nil, nil
		})
	}()
	time.Sleep(20 * time.Millisecond)
	_, err := Submit(ctx, e, "k", func(_ context.Context) (any, error) { return nil, nil })
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
			_, _ = Submit(context.Background(), e, "", func(_ context.Context) (any, error) {
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
		_, _ = Submit(context.Background(), e, "", func(_ context.Context) (any, error) {
			close(started)
			<-block
			return nil, nil
		})
	}()
	<-started
	_, err := Submit(context.Background(), e, "", func(_ context.Context) (any, error) { return nil, nil })
	if !errors.Is(err, ratelimit.ErrRateLimited) {
		t.Fatalf("got %v, want ErrRateLimited", err)
	}
	close(block)
}

func TestSubmitRPSLimitRejects(t *testing.T) {
	t.Parallel()
	e, _ := New(WithRPSLimiter(ratelimit.NewTokenBucket(1)))
	if _, err := Submit(context.Background(), e, "", func(context.Context) (any, error) {
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Submit(context.Background(), e, "", func(context.Context) (any, error) {
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
			v, _ := Submit(context.Background(), e, "shared", func(_ context.Context) (any, error) {
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
		_, _ = Submit(context.Background(), e, "shared", func(context.Context) (any, error) {
			close(started)
			<-release
			return 1, nil
		})
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := Submit(ctx, e, "shared", func(context.Context) (any, error) {
		t.Fatal("waiter must not execute")
		return nil, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want context deadline", err)
	}
	close(release)
}

func waitForSingleflightWaiters(t *testing.T, e *Engine, key string, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		e.sf.mu.Lock()
		call := e.sf.m[key]
		joined := call != nil && call.waiters == want
		e.sf.mu.Unlock()
		if joined {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("singleflight %q did not reach %d waiters", key, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSingleflightLeaderCancellationDoesNotCancelRemainingWaiter(t *testing.T) {
	t.Parallel()
	e, _ := New(WithIOPool(2), WithMaxInflight(4))
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	leaderDone := make(chan error, 1)
	go func() {
		_, err := Submit(leaderCtx, e, "shared-cancel", func(ctx context.Context) (any, error) {
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
		value, err := Submit(context.Background(), e, "shared-cancel", func(context.Context) (any, error) {
			t.Error("waiter must not execute")
			return nil, nil
		})
		waiterDone <- struct {
			value any
			err   error
		}{value, err}
	}()
	waitForSingleflightWaiters(t, e, "shared-cancel", 2)
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
	_, err := Submit(ctx, e, "deadline", func(runCtx context.Context) (any, error) {
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
		_, err := Submit(ctx1, e, "all-cancel", func(ctx context.Context) (any, error) {
			close(started)
			<-ctx.Done()
			executionCanceled <- ctx.Err()
			return nil, ctx.Err()
		})
		results <- err
	}()
	<-started
	go func() {
		_, err := Submit(ctx2, e, "all-cancel", func(context.Context) (any, error) {
			t.Error("waiter must not execute")
			return nil, nil
		})
		results <- err
	}()
	waitForSingleflightWaiters(t, e, "all-cancel", 2)
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
		_, _ = Submit(context.Background(), e, "", func(context.Context) (any, error) {
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
		_, err := Submit(ctx, e, "", func(context.Context) (any, error) { return nil, nil })
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
		_, err := Submit(ctx, e, "detached", func(context.Context) (any, error) {
			close(started)
			<-release
			return nil, nil
		})
		results <- err
	}
	go run(ctx1)
	<-started
	go run(ctx2)
	waitForSingleflightWaiters(t, e, "detached", 2)
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

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Run holds to the rate limit and recovers panics, and passes the breaker.
func TestRunPassesTheBreakerUnderTheRateLimit(t *testing.T) {
	t.Parallel()
	breaker := ratelimit.NewBreaker(1, time.Hour)
	breaker.Record(0, false)
	e, _ := New(WithIOPool(1), WithBreaker(breaker), WithRPSLimiter(ratelimit.NewTokenBucket(1)))
	if _, err := Run(context.Background(), e, func(context.Context) (any, error) { panic("boom") }); err == nil {
		t.Fatal("a panic was not recovered")
	}
	if _, err := Run(context.Background(), e, func(context.Context) (any, error) { return nil, nil }); !errors.Is(
		err, ratelimit.ErrRateLimited,
	) {
		t.Fatalf("a second run within the second passed the rate limit: %v", err)
	}
}

// A call every waiter left is canceled, and a caller coming while it winds
// down runs a call of its own instead of getting the canceled one's error.
func TestSingleflightLateCallerAfterAllWaitersLeft(t *testing.T) {
	t.Parallel()
	e, _ := New(WithIOPool(2), WithMaxInflight(4))
	ctx, cancel := context.WithCancel(context.Background())
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer close(release)
	go func() {
		_, _ = Submit(ctx, e, "late", func(ctx context.Context) (any, error) {
			close(started)
			<-ctx.Done()
			close(canceled)
			<-release // winds down slowly
			return nil, ctx.Err()
		})
	}()
	<-started
	cancel()
	<-canceled
	late, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	got, err := Submit(late, e, "late", func(context.Context) (any, error) { return "own", nil })
	if err != nil || got != "own" {
		t.Fatalf("late caller = %v, %v", got, err)
	}
}
