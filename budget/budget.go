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
	MaxScannedRows          int64 // deprecated alias
	MaxReturnedRows         int64
	MaxReturnedBytes        int64
	MaxSessionCost          int64
}

// Usage captures one invocation's observable resource usage.
type Usage struct {
	// ScannedRows is an actual observed scan count. Zero means unknown or none;
	// callers must not substitute ReturnedRows for it.
	ScannedRows int64
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

// Manager acquires scoped budgets.
type Manager interface {
	Acquire(context.Context, Scope) (Lease, error)
}

// ReservingManager supports atomic pre-execution budget reservations. Callers
// with cost estimates should prefer this interface over Manager.
type ReservingManager interface {
	Manager
	AcquireWithReservation(context.Context, Scope, Reservation) (Lease, error)
}

// SessionManager can discard all accounting for a disconnected session.
type SessionManager interface {
	Manager
	CloseSession(string)
}

type state struct {
	inflight int
	cost     int64
	reserved int64
	touched  time.Time
	closed   bool
}

// MemoryManager is a process-local manager with role and tenant overrides.
// Tenant limits take precedence over role limits.
type MemoryManager struct {
	mu        sync.Mutex
	roles     map[string]Limits
	tenants   map[string]Limits
	sessions  map[Scope]*state
	ttl       time.Duration
	maxStates int
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
		roles: cloneLimits(roles), tenants: cloneLimits(tenants), sessions: map[Scope]*state{},
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

func (m *MemoryManager) limits(scope Scope) Limits {
	if v, ok := m.tenants[scope.Tenant]; ok && scope.Tenant != "" {
		return v
	}
	return m.roles[scope.Role]
}

// Acquire implements Manager.
func (m *MemoryManager) Acquire(ctx context.Context, scope Scope) (Lease, error) {
	return m.AcquireWithReservation(ctx, scope, Reservation{})
}

// AcquireWithReservation atomically checks and holds estimated session cost
// until Complete reconciles it with actual usage.
func (m *MemoryManager) AcquireWithReservation(ctx context.Context, scope Scope, reservation Reservation) (Lease, error) {
	m.mu.Lock()
	now := time.Now()
	m.pruneLocked(now)
	limits := m.limits(scope)
	if reservation.Cost < 0 {
		reservation.Cost = 0
	}
	if reservation.EstimatedScannedRows < 0 {
		reservation.EstimatedScannedRows = 0
	}
	st := m.sessions[scope]
	if st == nil {
		if len(m.sessions) >= m.maxStates {
			m.mu.Unlock()
			return nil, ErrExceeded
		}
		st = &state{touched: now}
		m.sessions[scope] = st
	}
	if limits.MaxConcurrent > 0 && st.inflight >= limits.MaxConcurrent {
		m.mu.Unlock()
		return nil, ErrExceeded
	}
	maxEstimated := limits.MaxEstimatedScannedRows
	if maxEstimated == 0 {
		maxEstimated = limits.MaxScannedRows
	}
	if maxEstimated > 0 && reservation.EstimatedScannedRows > maxEstimated {
		m.mu.Unlock()
		return nil, ErrExceeded
	}
	if limits.MaxSessionCost > 0 &&
		(st.cost >= limits.MaxSessionCost ||
			st.reserved >= limits.MaxSessionCost-st.cost ||
			reservation.Cost > limits.MaxSessionCost-st.cost-st.reserved) {
		m.mu.Unlock()
		return nil, ErrExceeded
	}
	st.inflight++
	st.reserved += reservation.Cost
	st.touched = now
	m.mu.Unlock()
	runCtx := ctx
	cancel := func() {}
	if limits.MaxExecution > 0 {
		runCtx, cancel = context.WithTimeout(ctx, limits.MaxExecution)
	}
	return &lease{
		manager: m, scope: scope, ctx: runCtx, cancel: cancel, limits: limits,
		reservedCost: reservation.Cost,
	}, nil
}

func (m *MemoryManager) pruneLocked(now time.Time) {
	for scope, st := range m.sessions {
		if st.inflight == 0 && now.Sub(st.touched) >= m.ttl {
			delete(m.sessions, scope)
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
		if maxEstimated == 0 {
			maxEstimated = l.limits.MaxScannedRows
		}
		if (maxEstimated > 0 && (usage.EstimatedScannedRows > maxEstimated || usage.ScannedRows > maxEstimated)) ||
			(l.limits.MaxReturnedRows > 0 && usage.ReturnedRows > l.limits.MaxReturnedRows) ||
			(l.limits.MaxReturnedBytes > 0 && usage.ReturnedBytes > l.limits.MaxReturnedBytes) ||
			(l.limits.MaxSessionCost > 0 && sessionCost > l.limits.MaxSessionCost) {
			l.err = ErrExceeded
		}
	})
	return l.err
}
