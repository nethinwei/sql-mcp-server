// Package budget provides role/tenant scoped execution budgets.
package budget

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrExceeded is returned when a configured resource budget is exceeded.
var ErrExceeded = errors.New("resource budget exceeded")

// Scope identifies a caller budget. Tenant may be empty.
type Scope struct {
	Role    string
	Tenant  string
	Session string
}

// Limits is the effective budget for a scope. Zero values are unlimited.
type Limits struct {
	MaxConcurrent           int
	MaxExecution            time.Duration
	MaxEstimatedScannedRows int64
	MaxReturnedRows         int64
	MaxReturnedBytes        int64
	MaxSessionCost          int64
}

// Usage captures one invocation's observable resource usage.
type Usage struct {
	// EstimatedScannedRows is a planner estimate, not an observed scan count.
	EstimatedScannedRows int64
	ReturnedRows         int64
	ReturnedBytes        int64
	Duration             time.Duration
	Cost                 int64
}

// Reservation is a pre-execution estimate held against a session until its
// lease is completed and reconciled with actual Usage.
type Reservation struct {
	Cost                 int64
	EstimatedScannedRows int64
}

// Lease owns one concurrency slot.
type Lease interface {
	Context() context.Context
	Limits() Limits
	Complete(Usage) error
}

// Manager acquires scoped budgets, atomically holding a pre-execution
// reservation until the lease completes.
type Manager interface {
	AcquireWithReservation(context.Context, Scope, Reservation) (Lease, error)
}

type state struct {
	inflight int
	cost     int64
	reserved int64
	touched  time.Time
	closed   bool
}

// MemoryManager is a process-local manager with role and tenant overrides.
// Budgets are layered: a session's state is its principal's (Role) in one
// tenant; a tenant's maxConcurrent bounds the calls of all its sessions
// together, and its other limits tighten its sessions' field by field (see
// limits).
type MemoryManager struct {
	mu       sync.Mutex
	roles    map[string]Limits
	tenants  map[string]Limits
	sessions map[Scope]*state
	// inflight counts the calls running per tenant, across its sessions.
	inflight  map[string]int
	ttl       time.Duration
	maxStates int
	// lastSweep is when idle states were last swept (see pruneLocked).
	lastSweep time.Time
}

// New returns a manager. Empty maps mean no additional rejection.
func New(roles, tenants map[string]Limits) *MemoryManager {
	return NewWithBounds(roles, tenants, 30*time.Minute, 4096)
}

// NewWithBounds returns a manager with bounded idle scope state.
func NewWithBounds(roles, tenants map[string]Limits, ttl time.Duration, maxStates int) *MemoryManager {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	if maxStates <= 0 {
		maxStates = 4096
	}
	return &MemoryManager{
		roles: cloneLimits(roles), tenants: cloneLimits(tenants), sessions: map[Scope]*state{}, inflight: map[string]int{},
		ttl: ttl, maxStates: maxStates,
	}
}

func cloneLimits(values map[string]Limits) map[string]Limits {
	cloned := make(map[string]Limits, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

// UpdateLimits atomically replaces configured role and tenant limits while
// retaining all in-flight and accumulated session state.
func (m *MemoryManager) UpdateLimits(roles, tenants map[string]Limits) {
	m.mu.Lock()
	m.roles = cloneLimits(roles)
	m.tenants = cloneLimits(tenants)
	m.mu.Unlock()
}

// ConfiguredLimits returns copies of the current role and tenant limits.
func (m *MemoryManager) ConfiguredLimits() (map[string]Limits, map[string]Limits) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneLimits(m.roles), cloneLimits(m.tenants)
}

// limits are the role's limits tightened by the tenant's: per field, the
// smaller of the set (non-zero) values, so a tenant budget never lifts a role
// limit it leaves unset. The tenant's maxConcurrent is not among them: it
// bounds the tenant's calls together (see tenantConcurrency).
func (m *MemoryManager) limits(scope Scope) Limits {
	l := m.roles[scope.Role]
	t, ok := m.tenants[scope.Tenant]
	if !ok || scope.Tenant == "" {
		return l
	}
	return Limits{
		MaxConcurrent:           l.MaxConcurrent,
		MaxExecution:            Stricter(l.MaxExecution, t.MaxExecution),
		MaxEstimatedScannedRows: Stricter(l.MaxEstimatedScannedRows, t.MaxEstimatedScannedRows),
		MaxReturnedRows:         Stricter(l.MaxReturnedRows, t.MaxReturnedRows),
		MaxReturnedBytes:        Stricter(l.MaxReturnedBytes, t.MaxReturnedBytes),
		MaxSessionCost:          Stricter(l.MaxSessionCost, t.MaxSessionCost),
	}
}

// Stricter is the lower of two limits, where zero (or less) is unlimited:
// how a role's, a user's or a tenant's limit narrows another.
func Stricter[T int | int64 | time.Duration](a, b T) T {
	switch {
	case b <= 0:
		return a
	case a <= 0:
		return b
	}
	return min(a, b)
}

// Acquire acquires a lease without a reservation.
func (m *MemoryManager) Acquire(ctx context.Context, scope Scope) (Lease, error) {
	return m.AcquireWithReservation(ctx, scope, Reservation{})
}

// AcquireWithReservation atomically checks and holds estimated session cost
// until Complete reconciles it with actual usage.
func (m *MemoryManager) AcquireWithReservation(
	ctx context.Context,
	scope Scope,
	reservation Reservation,
) (Lease, error) {
	m.mu.Lock()
	now := time.Now()
	m.pruneLocked(now)
	limits := m.limits(scope)
	reservation = normalizeReservation(reservation)
	st, err := m.acquireStateLocked(scope, now)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	if err := checkReservationLimits(limits, st, reservation); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	if max := m.tenantConcurrency(scope.Tenant); max > 0 && m.inflight[scope.Tenant] >= max {
		m.mu.Unlock()
		return nil, ErrExceeded
	}
	if scope.Tenant != "" {
		m.inflight[scope.Tenant]++
	}
	st.inflight++
	st.reserved += reservation.Cost
	st.touched = now
	m.mu.Unlock()
	return m.newLease(ctx, scope, limits, reservation.Cost), nil
}

// tenantConcurrency is the bound on the calls of tenant's sessions
// together (0: none).
func (m *MemoryManager) tenantConcurrency(tenant string) int {
	if tenant == "" {
		return 0
	}
	return m.tenants[tenant].MaxConcurrent
}

func normalizeReservation(reservation Reservation) Reservation {
	if reservation.Cost < 0 {
		reservation.Cost = 0
	}
	if reservation.EstimatedScannedRows < 0 {
		reservation.EstimatedScannedRows = 0
	}
	return reservation
}

func (m *MemoryManager) acquireStateLocked(scope Scope, now time.Time) (*state, error) {
	st := m.sessions[scope]
	if st != nil && st.inflight == 0 && now.Sub(st.touched) >= m.ttl {
		delete(m.sessions, scope) // idle past ttl: starts again, swept or not
		st = nil
	}
	if st == nil {
		if len(m.sessions) >= m.maxStates {
			return nil, ErrExceeded
		}
		st = &state{touched: now}
		m.sessions[scope] = st
	}
	return st, nil
}

func checkReservationLimits(limits Limits, st *state, reservation Reservation) error {
	if limits.MaxConcurrent > 0 && st.inflight >= limits.MaxConcurrent {
		return ErrExceeded
	}
	if maxEstimated := limits.MaxEstimatedScannedRows; maxEstimated > 0 &&
		reservation.EstimatedScannedRows > maxEstimated {
		return ErrExceeded
	}
	if limits.MaxSessionCost > 0 &&
		(st.cost >= limits.MaxSessionCost ||
			st.reserved >= limits.MaxSessionCost-st.cost ||
			reservation.Cost > limits.MaxSessionCost-st.cost-st.reserved) {
		return ErrExceeded
	}
	return nil
}

func (m *MemoryManager) newLease(
	ctx context.Context,
	scope Scope,
	limits Limits,
	reservedCost int64,
) Lease {
	runCtx := ctx
	cancel := func() {}
	if limits.MaxExecution > 0 {
		runCtx, cancel = context.WithTimeout(ctx, limits.MaxExecution)
	}
	return &lease{
		manager: m, scope: scope, ctx: runCtx, cancel: cancel, limits: limits,
		reservedCost: reservedCost,
	}
}

// pruneLocked drops the states idle for ttl, sweeping them at most every
// quarter of it (a sweep reads every state, under the lock every call
// takes), and the least recently used ones past maxStates.
func (m *MemoryManager) pruneLocked(now time.Time) {
	if now.Sub(m.lastSweep) >= m.ttl/4 {
		m.lastSweep = now
		for scope, st := range m.sessions {
			if st.inflight == 0 && now.Sub(st.touched) >= m.ttl {
				delete(m.sessions, scope)
			}
		}
	}
	for len(m.sessions) >= m.maxStates {
		var oldestScope Scope
		var oldest *state
		for scope, st := range m.sessions {
			if st.inflight == 0 && (oldest == nil || st.touched.Before(oldest.touched)) {
				oldestScope, oldest = scope, st
			}
		}
		if oldest == nil {
			break
		}
		delete(m.sessions, oldestScope)
	}
}

// CloseSession discards idle accounting for session and marks active scopes for
// removal when their last lease completes.
func (m *MemoryManager) CloseSession(session string) {
	if session == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for scope, st := range m.sessions {
		if scope.Session == session {
			st.cost = 0
			if st.inflight == 0 {
				delete(m.sessions, scope)
			} else {
				st.closed = true
			}
		}
	}
}

type lease struct {
	manager      *MemoryManager
	scope        Scope
	ctx          context.Context
	cancel       context.CancelFunc
	limits       Limits
	reservedCost int64
	once         sync.Once
	err          error
}

func (l *lease) Context() context.Context { return l.ctx }
func (l *lease) Limits() Limits           { return l.limits }

func (l *lease) Complete(usage Usage) error {
	l.once.Do(func() {
		l.cancel()
		l.manager.mu.Lock()
		st := l.manager.sessions[l.scope]
		st.inflight--
		if tenant := l.scope.Tenant; tenant != "" {
			if l.manager.inflight[tenant]--; l.manager.inflight[tenant] <= 0 {
				delete(l.manager.inflight, tenant)
			}
		}
		st.reserved -= l.reservedCost
		if usage.Cost > 0 {
			st.cost += usage.Cost
		}
		st.touched = time.Now()
		sessionCost := st.cost
		if st.closed && st.inflight == 0 {
			delete(l.manager.sessions, l.scope)
		}
		l.manager.mu.Unlock()
		maxEstimated := l.limits.MaxEstimatedScannedRows
		if (maxEstimated > 0 && usage.EstimatedScannedRows > maxEstimated) ||
			(l.limits.MaxReturnedRows > 0 && usage.ReturnedRows > l.limits.MaxReturnedRows) ||
			(l.limits.MaxReturnedBytes > 0 && usage.ReturnedBytes > l.limits.MaxReturnedBytes) ||
			(l.limits.MaxSessionCost > 0 && sessionCost > l.limits.MaxSessionCost) {
			l.err = ErrExceeded
		}
	})
	return l.err
}
