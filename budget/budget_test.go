package budget

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryManagerConcurrencyAndReturnedRows(t *testing.T) {
	m := New(map[string]Limits{"reader": {MaxConcurrent: 1, MaxReturnedRows: 2}}, nil)
	lease, err := m.Acquire(context.Background(), Scope{Role: "reader"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Acquire(context.Background(), Scope{Role: "reader"}); !errors.Is(err, ErrExceeded) {
		t.Fatalf("second acquire error = %v", err)
	}
	if err := lease.Complete(Usage{ReturnedRows: 3}); !errors.Is(err, ErrExceeded) {
		t.Fatalf("complete error = %v", err)
	}
}

func TestMemoryManagerTenantOverrideAndTimeout(t *testing.T) {
	m := New(
		map[string]Limits{"reader": {MaxConcurrent: 1}},
		map[string]Limits{"acme": {MaxExecution: time.Millisecond}},
	)
	lease, err := m.Acquire(context.Background(), Scope{Role: "reader", Tenant: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Complete(Usage{})
	select {
	case <-lease.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("tenant timeout was not applied")
	}
}

func TestMemoryManagerSeparatesAndClosesSessions(t *testing.T) {
	m := New(map[string]Limits{"reader": {MaxSessionCost: 2}}, nil)
	first, err := m.Acquire(context.Background(), Scope{Role: "reader", Session: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Complete(Usage{Cost: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Acquire(context.Background(), Scope{Role: "reader", Session: "a"}); !errors.Is(err, ErrExceeded) {
		t.Fatalf("same session acquire error = %v", err)
	}
	other, err := m.Acquire(context.Background(), Scope{Role: "reader", Session: "b"})
	if err != nil {
		t.Fatalf("other session must have independent cost: %v", err)
	}
	_ = other.Complete(Usage{})
	m.CloseSession("a")
	again, err := m.Acquire(context.Background(), Scope{Role: "reader", Session: "a"})
	if err != nil {
		t.Fatalf("closed session state was not cleared: %v", err)
	}
	_ = again.Complete(Usage{})
}

func TestMemoryManagerStateTTLAndCapacity(t *testing.T) {
	m := NewWithBounds(nil, nil, time.Millisecond, 1)
	one, err := m.Acquire(context.Background(), Scope{Session: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Acquire(context.Background(), Scope{Session: "two"}); !errors.Is(err, ErrExceeded) {
		t.Fatalf("active state capacity error = %v", err)
	}
	_ = one.Complete(Usage{})
	time.Sleep(2 * time.Millisecond)
	two, err := m.Acquire(context.Background(), Scope{Session: "two"})
	if err != nil {
		t.Fatalf("expired idle state was not pruned: %v", err)
	}
	_ = two.Complete(Usage{})
}

func TestMemoryManagerUpdateLimitsPreservesSessionState(t *testing.T) {
	m := New(map[string]Limits{"reader": {MaxSessionCost: 10, MaxConcurrent: 2}}, nil)
	lease, err := m.Acquire(context.Background(), Scope{Role: "reader", Session: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Complete(Usage{Cost: 6}); err != nil {
		t.Fatal(err)
	}
	m.UpdateLimits(map[string]Limits{"reader": {MaxSessionCost: 5, MaxConcurrent: 1}}, nil)
	if _, err := m.Acquire(context.Background(), Scope{Role: "reader", Session: "s"}); !errors.Is(err, ErrExceeded) {
		t.Fatalf("updated limit ignored preserved cost: %v", err)
	}
	fresh, err := m.Acquire(context.Background(), Scope{Role: "reader", Session: "fresh"})
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Limits().MaxConcurrent != 1 {
		t.Fatalf("fresh limits = %+v", fresh.Limits())
	}
	_ = fresh.Complete(Usage{})
}
