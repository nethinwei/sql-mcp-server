package ratelimit

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestAdaptiveAcquireRelease(t *testing.T) {
	t.Parallel()
	a := NewAdaptive(2, 1, 4, time.Second)
	if err := a.Acquire(); err != nil {
		t.Fatal(err)
	}
	if err := a.Acquire(); err != nil {
		t.Fatal(err)
	}
	if err := a.Acquire(); err != ErrRateLimited {
		t.Fatalf("got %v, want ErrRateLimited", err)
	}
	a.Release()
	if err := a.Acquire(); err != nil {
		t.Fatal("should acquire after release")
	}
}

func TestTokenBucketRejectsBurstOverflow(t *testing.T) {
	limiter := NewTokenBucket(1)
	if err := limiter.Allow(); err != nil {
		t.Fatal(err)
	}
	if err := limiter.Allow(); err != ErrRateLimited {
		t.Fatalf("second request error = %v", err)
	}
}

func TestAdaptiveAIMD(t *testing.T) {
	t.Parallel()
	a := NewAdaptive(4, 1, 8, time.Second)
	// success -> additive increase
	a.OnResult(nil, time.Millisecond)
	if a.Limit() != 5 {
		t.Fatalf("limit = %d, want 5", a.Limit())
	}
	// error -> multiplicative decrease
	a.OnResult(errors.New("db error"), time.Millisecond)
	if a.Limit() != 2 {
		t.Fatalf("limit = %d, want 2", a.Limit())
	}
	// slow, but sent before that decrease: already answered for
	a.OnResult(nil, 2*time.Second)
	if a.Limit() != 2 {
		t.Fatalf("limit = %d, want 2", a.Limit())
	}
	time.Sleep(2 * time.Millisecond)
	// an error sent after it -> decrease
	a.OnResult(errors.New("db error"), time.Microsecond)
	if a.Limit() != 1 {
		t.Fatalf("limit = %d, want 1 (min)", a.Limit())
	}
}

// Requests in flight together that all come back slow are one overload, met
// with one decrease, not one halving each.
func TestAdaptiveBurstDecreasesOnce(t *testing.T) {
	t.Parallel()
	a := NewAdaptive(64, 1, 64, 100*time.Millisecond)
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() { a.OnResult(nil, time.Second) })
	}
	wg.Wait()
	if a.Limit() != 32 {
		t.Fatalf("limit = %d after one slow burst, want 32", a.Limit())
	}
}

func TestBreakerOpensAndRecovers(t *testing.T) {
	t.Parallel()
	b := NewBreaker(2, 50*time.Millisecond)
	if _, err := b.Allow(); err != nil {
		t.Fatal(err)
	}
	b.Record(false)
	if b.IsOpen() {
		t.Fatal("should not open after 1 failure")
	}
	b.Record(false)
	if !b.IsOpen() {
		t.Fatal("should open after threshold failures")
	}
	if _, err := b.Allow(); err != ErrCircuitOpen {
		t.Fatalf("got %v, want ErrCircuitOpen", err)
	}
	time.Sleep(60 * time.Millisecond)
	if _, err := b.Allow(); err != nil {
		t.Fatal("should allow after cooldown (half-open)")
	}
	b.Record(true)
	if b.IsOpen() {
		t.Fatal("should close after success")
	}
}

// A breaker past its cooldown lets one probe through at a time, not every
// request; the probe's result closes or reopens it, and a probe that ends
// without a result frees its place.
func TestBreakerHalfOpenProbes(t *testing.T) {
	t.Parallel()
	b := NewBreaker(1, 20*time.Millisecond)
	b.Record(false)
	time.Sleep(30 * time.Millisecond)
	probe, err := b.Allow()
	if err != nil || probe == 0 {
		t.Fatalf("probe refused: %v", err)
	}
	if _, err := b.Allow(); err != ErrCircuitOpen {
		t.Fatalf("a second request passed while probing: %v", err)
	}
	b.Release(probe)
	if _, err := b.Allow(); err != nil {
		t.Fatalf("a released probe kept its place: %v", err)
	}
	b.Record(false)
	if _, err := b.Allow(); err != ErrCircuitOpen {
		t.Fatalf("a failed probe must reopen: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	if _, err := b.Allow(); err != nil {
		t.Fatal(err)
	}
	b.Record(true)
	for range 3 {
		if _, err := b.Allow(); err != nil {
			t.Fatalf("closed after a good probe: %v", err)
		}
	}
}

// A request let through before the probe, ending without a result while the
// probe runs, leaves the probe its place: one probe at a time.
func TestBreakerEarlierRequestsLeaveTheProbe(t *testing.T) {
	t.Parallel()
	b := NewBreaker(1, 20*time.Millisecond)
	earlier, err := b.Allow() // still running as the breaker opens
	if err != nil || earlier != 0 {
		t.Fatalf("closed breaker = %d, %v", earlier, err)
	}
	b.Record(false)
	time.Sleep(30 * time.Millisecond)
	first, err := b.Allow()
	if err != nil {
		t.Fatalf("probe refused: %v", err)
	}
	b.Release(earlier) // the earlier request, cancelled
	if _, err := b.Allow(); err != ErrCircuitOpen {
		t.Fatalf("a second probe passed: %v", err)
	}
	b.Record(false) // another request failed: open again while the first probe runs
	time.Sleep(30 * time.Millisecond)
	if _, err := b.Allow(); err != nil {
		t.Fatalf("the next probe refused: %v", err)
	}
	b.Release(first) // the first probe ends without a result
	if _, err := b.Allow(); err != ErrCircuitOpen {
		t.Fatalf("an earlier probe freed the next one's place: %v", err)
	}
}
