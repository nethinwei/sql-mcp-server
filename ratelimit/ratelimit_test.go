package ratelimit

import (
	"errors"
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
	// slow -> decrease
	a.OnResult(nil, 2*time.Second)
	if a.Limit() != 1 {
		t.Fatalf("limit = %d, want 1 (min)", a.Limit())
	}
}

func TestBreakerOpensAndRecovers(t *testing.T) {
	t.Parallel()
	b := NewBreaker(2, 50*time.Millisecond)
	if err := b.Allow(); err != nil {
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
	if err := b.Allow(); err != ErrCircuitOpen {
		t.Fatalf("got %v, want ErrCircuitOpen", err)
	}
	time.Sleep(60 * time.Millisecond)
	if err := b.Allow(); err != nil {
		t.Fatal("should allow after cooldown (half-open)")
	}
	b.Record(true)
	if b.IsOpen() {
		t.Fatal("should close after success")
	}
}
