package budget

import (
	"context"
	"errors"
	"math/rand/v2"
	"slices"
	"strconv"
	"sync"
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

// A tenant budget tightens the role's, field by field: a limit the tenant
// leaves unset keeps the role's, and the smaller of two set limits applies.
func TestMemoryManagerTenantLimitsTightenRoleLimits(t *testing.T) {
	m := New(
		map[string]Limits{"reader": {MaxConcurrent: 1, MaxReturnedRows: 10}},
		map[string]Limits{"acme": {MaxExecution: time.Minute, MaxReturnedRows: 100, MaxReturnedBytes: 5}},
	)
	scope := Scope{Role: "reader", Tenant: "acme", Session: "s"}
	lease, err := m.Acquire(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Complete(Usage{})
	want := Limits{MaxConcurrent: 1, MaxExecution: time.Minute, MaxReturnedRows: 10, MaxReturnedBytes: 5}
	if got := lease.Limits(); got != want {
		t.Fatalf("limits = %+v, want %+v", got, want)
	}
	if _, err := m.Acquire(context.Background(), scope); !errors.Is(err, ErrExceeded) {
		t.Fatalf("the role's concurrency limit was lifted by a tenant budget: %v", err)
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

// A caller opening sessions without end cannot lock others out: a full state
// table makes room by dropping the least recently used idle state, before its
// TTL.
func TestMemoryManagerFullTableEvictsIdleStates(t *testing.T) {
	m := NewWithBounds(nil, nil, time.Hour, 2)
	for _, session := range []string{"flood-1", "flood-2", "flood-3"} {
		lease, err := m.Acquire(context.Background(), Scope{Role: "flood", Session: session})
		if err != nil {
			t.Fatalf("%s: %v", session, err)
		}
		_ = lease.Complete(Usage{})
	}
	other, err := m.Acquire(context.Background(), Scope{Role: "other", Session: "s"})
	if err != nil {
		t.Fatalf("a full table of idle states refused another caller: %v", err)
	}
	_ = other.Complete(Usage{})
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

func runConcurrentReservations(
	t *testing.T,
	m *MemoryManager,
	scope Scope,
	workers int,
) []Lease {
	t.Helper()
	start := make(chan struct{})
	results := make(chan Lease, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			lease, err := m.AcquireWithReservation(
				context.Background(),
				scope,
				Reservation{Cost: 6},
			)
			if err != nil {
				results <- nil
				return
			}
			results <- lease
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var acquired []Lease
	for lease := range results {
		if lease != nil {
			acquired = append(acquired, lease)
		}
	}
	return acquired
}

func TestMemoryManagerConcurrentReservationsAreAtomic(t *testing.T) {
	m := New(map[string]Limits{"reader": {MaxSessionCost: 10}}, nil)
	scope := Scope{Role: "reader", Session: "shared"}

	acquired := runConcurrentReservations(t, m, scope, 16)
	if len(acquired) != 1 {
		t.Fatalf("acquired reservations = %d, want 1", len(acquired))
	}
	if err := acquired[0].Complete(Usage{Cost: 4}); err != nil {
		t.Fatal(err)
	}

	next, err := m.AcquireWithReservation(context.Background(), scope, Reservation{Cost: 6})
	if err != nil {
		t.Fatalf("reconciled reservation was not released: %v", err)
	}
	if err := next.Complete(Usage{Cost: 6}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Acquire(context.Background(), scope); !errors.Is(err, ErrExceeded) {
		t.Fatalf("actual cost at limit was not enforced: %v", err)
	}
}

func TestMemoryManagerChecksEstimatedScannedRows(t *testing.T) {
	m := New(map[string]Limits{"reader": {MaxEstimatedScannedRows: 10}}, nil)
	scope := Scope{Role: "reader"}
	if _, err := m.AcquireWithReservation(
		context.Background(),
		scope,
		Reservation{EstimatedScannedRows: 11},
	); !errors.Is(err, ErrExceeded) {
		t.Fatalf("estimated scan error = %v", err)
	}

	lease, err := m.AcquireWithReservation(
		context.Background(),
		scope,
		Reservation{EstimatedScannedRows: 5},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Complete(Usage{EstimatedScannedRows: 11, ReturnedRows: 1}); !errors.Is(err, ErrExceeded) {
		t.Fatalf("completed estimate error = %v", err)
	}
}

// A tenant's maxConcurrent bounds its sessions' calls together: opening
// another session does not get round it, and other tenants are not affected.
func TestTenantConcurrencySpansSessions(t *testing.T) {
	t.Parallel()
	m := New(nil, map[string]Limits{"acme": {MaxConcurrent: 1}})
	ctx := context.Background()
	held, err := m.Acquire(ctx, Scope{Role: "reader", Tenant: "acme", Session: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Acquire(ctx, Scope{Role: "writer", Tenant: "acme", Session: "b"}); !errors.Is(err, ErrExceeded) {
		t.Fatalf("another session of the tenant = %v", err)
	}
	other, err := m.Acquire(ctx, Scope{Role: "reader", Tenant: "globex", Session: "c"})
	if err != nil {
		t.Fatalf("another tenant = %v", err)
	}
	_ = other.Complete(Usage{})
	_ = held.Complete(Usage{})
	again, err := m.Acquire(ctx, Scope{Role: "writer", Tenant: "acme", Session: "b"})
	if err != nil {
		t.Fatalf("after the call ended = %v", err)
	}
	_ = again.Complete(Usage{})
}

func TestStricter(t *testing.T) {
	t.Parallel()
	for _, c := range [][3]int64{
		{0, 0, 0}, {5, 0, 5}, {0, 5, 5}, {5, 3, 3}, {3, 5, 3}, {-1, 5, 5}, {5, -1, 5}, {-1, -1, -1},
	} {
		if got := Stricter(c[0], c[1]); got != c[2] {
			t.Errorf("Stricter(%d, %d) = %d, want %d", c[0], c[1], got, c[2])
		}
	}
}

// A full table never drops the state of the scope acquiring: its session
// cost stays.
func TestMemoryManagerFullTableKeepsTheCallersState(t *testing.T) {
	m := NewWithBounds(map[string]Limits{"r": {MaxSessionCost: 10}}, nil, time.Hour, 1)
	scope := Scope{Role: "r", Session: "s"}
	for i := range 3 {
		lease, err := m.Acquire(context.Background(), scope)
		if i == 2 {
			if !errors.Is(err, ErrExceeded) {
				t.Fatalf("the session's cost was dropped: %v", err)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		_ = lease.Complete(Usage{Cost: 6})
	}
}

// Random acquires, completions and closed sessions keep the table bounded
// and the idle list exactly the states no call runs in.
func TestMemoryManagerIdleListInvariants(t *testing.T) {
	m := NewWithBounds(nil, nil, time.Hour, 4)
	rng := rand.New(rand.NewPCG(1, 2))
	var leases []Lease
	for step := range 5000 {
		switch rng.IntN(3) {
		case 0:
			scope := Scope{Role: "r", Session: strconv.Itoa(rng.IntN(8))}
			if lease, err := m.Acquire(context.Background(), scope); err == nil {
				leases = append(leases, lease)
			}
		case 1:
			if len(leases) > 0 {
				i := rng.IntN(len(leases))
				_ = leases[i].Complete(Usage{Cost: 1})
				leases = slices.Delete(leases, i, i+1)
			}
		default:
			m.CloseSession(strconv.Itoa(rng.IntN(8)))
		}
		m.mu.Lock()
		idle := 0
		for scope, st := range m.sessions {
			if (st.inflight == 0) != (st.idle != nil) || st.idle != nil && st.idle.Value.(Scope) != scope {
				m.mu.Unlock()
				t.Fatalf("step %d: state of %v: inflight %d, idle %v", step, scope, st.inflight, st.idle)
			}
			if st.idle != nil {
				idle++
			}
		}
		if len(m.sessions) > m.maxStates || m.idle.Len() != idle {
			m.mu.Unlock()
			t.Fatalf("step %d: %d states, %d listed idle, %d idle", step, len(m.sessions), m.idle.Len(), idle)
		}
		m.mu.Unlock()
	}
}
