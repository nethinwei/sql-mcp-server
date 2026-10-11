package engine

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Engines sharing a quota share its IO slots: while one engine's work holds
// the only slot, the other's waits, until the slot is freed or the quota
// grows.
func TestEnginesShareQuotaSlots(t *testing.T) {
	t.Parallel()
	quota := NewQuota(1)
	old, _ := New(WithQuota(quota))
	next, _ := New(WithQuota(quota))
	hold, held := make(chan struct{}), make(chan struct{})
	go func() {
		_, _ = Submit(context.Background(), old, "", func(context.Context) (any, error) {
			close(held)
			<-hold
			return nil, nil
		})
	}()
	<-held
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	nothing := func(context.Context) (any, error) { return nil, nil }
	if _, err := Submit(ctx, next, "", nothing); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second engine ran past the shared slot: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := Submit(context.Background(), next, "", nothing)
		done <- err
	}()
	quota.Update(2, nil, nil, nil)
	if err := <-done; err != nil {
		t.Fatalf("growing the quota must admit the waiting work: %v", err)
	}
	close(hold)
}

// Work that gives up waiting leaves no slot taken.
func TestQuotaWaiterCanceled(t *testing.T) {
	t.Parallel()
	quota := NewQuota(1)
	if err := quota.io.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := quota.io.Acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("acquire = %v", err)
	}
	quota.io.Release()
	if st := quota.io.Stats(); st.Used != 0 || st.Waiting != 0 {
		t.Fatalf("used %d, waiters %d", st.Used, st.Waiting)
	}
}
