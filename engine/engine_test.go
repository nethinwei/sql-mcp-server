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
	if err := e.Submit(context.Background(), "k", func(_ context.Context) error {
		ran = true
		return nil
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
		_ = e.Submit(context.Background(), "k1", func(_ context.Context) error {
			<-block
			return nil
		})
	}()
	time.Sleep(20 * time.Millisecond)
	err := e.Submit(context.Background(), "k2", func(_ context.Context) error { return nil })
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
			_ = e.Submit(context.Background(), key, func(_ context.Context) error {
				calls.Add(1)
				time.Sleep(20 * time.Millisecond)
				return nil
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
	err := e.Submit(context.Background(), "k", func(_ context.Context) error {
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
	_ = e.Submit(context.Background(), "k", func(_ context.Context) error {
		return errors.New("fail")
	})
	err := e.Submit(context.Background(), "k2", func(_ context.Context) error { return nil })
	if !errors.Is(err, ratelimit.ErrCircuitOpen) {
		t.Fatalf("got %v, want ErrCircuitOpen", err)
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
		_ = e.Submit(context.Background(), "occupier", func(_ context.Context) error {
			<-block
			return nil
		})
	}()
	time.Sleep(20 * time.Millisecond)
	err := e.Submit(ctx, "k", func(_ context.Context) error { return nil })
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

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
