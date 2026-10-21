package tool

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/bus"
	"github.com/nethinwei/sql-mcp-server/core/store"
)

var (
	ErrTransactionNotFound = errors.New("tool: transaction not found")
	ErrTransactionScope    = errors.New("tool: transaction scope mismatch")
	ErrTransactionCapacity = errors.New("tool: transaction capacity reached")
	// ErrTransactionStale rejects statements in a transaction whose datasource
	// a reload has since pointed at another connection: the configuration they
	// would be planned with no longer describes the database it runs on.
	ErrTransactionStale = errors.New("tool: transaction datasource was reconfigured; roll it back and begin again")
)

// Caller is who holds a transaction: the transport session it was begun in
// and the authorization identity it was begun under. A statement of the
// transaction must come from the same caller.
type Caller struct {
	Session string
	Role    string
	Subject map[string]any
}

// callerOf is the caller tc serves.
func callerOf(tc Context) Caller {
	return Caller{Session: tc.Session, Role: tc.Role, Subject: tc.Subject}
}

// scope is the authorization identity of the caller, the key its
// transactions are counted under.
func (c Caller) scope() string { return scopeKey(c.Role, c.Subject) }

type transactionHandle struct {
	tx         store.Tx
	connection store.TxBeginner // the connection begun on
	session    string
	scope      string // the caller's, see Caller.scope
	datasource string
	dirty      map[CacheTarget]struct{}
	expires    time.Time
	stopTimer  func() bool
	cancel     context.CancelFunc
	mu         sync.Mutex
	closed     bool
	// idle is the idle timeout (0: none); used is when a statement last
	// started or ended (unix nanoseconds) and running the statements
	// running; stopIdle stops the idle check (m.mu).
	idle     time.Duration
	used     atomic.Int64
	running  atomic.Int32
	stopIdle func() bool
}

// TransactionManager owns bounded, expiring explicit transaction handles.
// Tokens are unguessable and additionally bound to role, subject, and source.
type TransactionManager struct {
	mu      sync.Mutex
	handles map[string]*transactionHandle
	open    map[string]int // handles by scope
	// byConnection counts handles by the connection they hold, at most
	// perConnection each (0: no bound), so open transactions leave queries a
	// share of each connection pool.
	byConnection  map[store.TxBeginner]int
	perConnection int
	ttl           time.Duration
	idle          time.Duration // the idle timeout of transactions begun from now on
	maxOpen       int
	closed        bool
	bus           *bus.Bus // runs the expiry timers
	// ending holds the handles dropped from handles whose rollback or commit
	// is still running (outside mu); a lookup of their token waits for it to
	// finish.
	ending map[string]chan struct{}
	// tombstones record how recent transactions ended, oldest first in
	// buried (see buryLocked).
	tombstones map[string]tombstone
	buried     []string
}

func NewTransactionManager(ttl time.Duration, maxOpen int) *TransactionManager {
	return &TransactionManager{
		handles: make(map[string]*transactionHandle), open: map[string]int{}, ttl: ttl, maxOpen: maxOpen,
		ending: map[string]chan struct{}{}, byConnection: map[store.TxBeginner]int{},
		tombstones: map[string]tombstone{},
	}
}

// UseBus runs the expiry timers as background tasks of b.
func (m *TransactionManager) UseBus(b *bus.Bus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bus = b
}

// SetConnectionLimit bounds the transactions open on one connection at once
// (0: no bound), for every later begin.
func (m *TransactionManager) SetConnectionLimit(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.perConnection = n
}

// SetIdleTimeout rolls back the transactions begun from now on once no
// statement used them for d (0: never).
func (m *TransactionManager) SetIdleTimeout(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.idle = d
}

// UpdateLimits applies new limits: ttl to the transactions begun from now
// on, maxOpen to every later begin.
func (m *TransactionManager) UpdateLimits(ttl time.Duration, maxOpen int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ttl, m.maxOpen = ttl, maxOpen
}

func (m *TransactionManager) Begin(
	ctx context.Context,
	beginner store.TxBeginner,
	caller Caller,
	datasource string,
	opts *store.TxOptions,
) (string, error) {
	scope := caller.scope()
	if err := m.checkBeginCapacity(scope, beginner); err != nil {
		return "", err
	}
	ttl, _ := m.Configuration()
	lifetimeCtx, cancel := beginLifetimeContext(ctx, ttl)
	tx, err := beginTransaction(ctx, lifetimeCtx, cancel, beginner, opts)
	if err != nil {
		return "", err
	}
	token, err := newTransactionToken()
	if err != nil {
		cancel()
		_ = tx.Rollback()
		return "", err
	}
	handle := &transactionHandle{
		tx: tx, connection: beginner, session: caller.Session, scope: scope,
		datasource: datasource, dirty: make(map[CacheTarget]struct{}), cancel: cancel,
	}
	if ttl > 0 {
		handle.expires = time.Now().Add(ttl)
	}
	if err := m.registerHandle(scope, token, handle); err != nil {
		return "", err
	}
	return token, nil
}

func (m *TransactionManager) checkBeginCapacity(scope string, connection store.TxBeginner) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrTransactionNotFound
	}
	return m.capacityLocked(scope, connection)
}

// capacityLocked refuses a transaction past the bound of its scope or of
// its connection; m.mu is held.
func (m *TransactionManager) capacityLocked(scope string, connection store.TxBeginner) error {
	if m.maxOpen > 0 && m.open[scope] >= m.maxOpen {
		return ErrTransactionCapacity
	}
	if m.perConnection > 0 && m.byConnection[connection] >= m.perConnection {
		return fmt.Errorf("%w on this connection", ErrTransactionCapacity)
	}
	return nil
}

func beginLifetimeContext(ctx context.Context, ttl time.Duration) (context.Context, context.CancelFunc) {
	lifetimeCtx := context.WithoutCancel(ctx)
	if ttl > 0 {
		return context.WithTimeout(lifetimeCtx, ttl)
	}
	return context.WithCancel(lifetimeCtx)
}

func beginTransaction(
	ctx context.Context,
	lifetimeCtx context.Context,
	cancel context.CancelFunc,
	beginner store.TxBeginner,
	opts *store.TxOptions,
) (store.Tx, error) {
	stopRequestCancel := context.AfterFunc(ctx, cancel)
	tx, err := beginner.BeginTx(lifetimeCtx, opts)
	stopRequestCancel()
	if err != nil {
		cancel()
		if requestErr := ctx.Err(); requestErr != nil {
			return nil, requestErr
		}
		return nil, WrapDBError(err)
	}
	if err := ctx.Err(); err != nil {
		cancel()
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func newTransactionToken() (string, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(tokenBytes), nil
}

func (m *TransactionManager) registerHandle(scope, token string, handle *transactionHandle) error {
	if err := m.addHandle(scope, token, handle); err != nil {
		handle.abort() // outside m.mu: a rollback waits for the database
		return err
	}
	return nil
}

func (m *TransactionManager) addHandle(scope, token string, handle *transactionHandle) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrTransactionNotFound
	}
	if err := m.capacityLocked(scope, handle.connection); err != nil {
		return err
	}
	m.handles[token] = handle
	m.open[scope]++
	m.byConnection[handle.connection]++
	if !handle.expires.IsZero() {
		handle.stopTimer = m.bus.AfterFunc("transaction expiry", time.Until(handle.expires), func() {
			m.expire(token, handle)
		})
	}
	handle.touch()
	if handle.idle = m.idle; handle.idle > 0 {
		m.watchIdleLocked(token, handle, handle.idle)
	}
	return nil
}

// removeLocked drops a handle; m.mu is held.
func (m *TransactionManager) removeLocked(token string, handle *transactionHandle) {
	delete(m.handles, token)
	if m.open[handle.scope]--; m.open[handle.scope] <= 0 {
		delete(m.open, handle.scope)
	}
	if m.byConnection[handle.connection]--; m.byConnection[handle.connection] <= 0 {
		delete(m.byConnection, handle.connection)
	}
}

// expire drops an expired handle and rolls it back.
func (m *TransactionManager) expire(token string, handle *transactionHandle) {
	m.end(token, handle, OutcomeExpired)
}

// endLocked drops a handle and records it as ending; m.mu is held. The
// caller runs the returned rollback after releasing m.mu: it waits for the
// database, and for a statement of the transaction still running, which must
// not hold up the other transactions. A lookup that misses the token
// meanwhile waits for it, so "not found" always means the transaction ended.
func (m *TransactionManager) endLocked(token string, handle *transactionHandle, outcome string) (rollback func()) {
	end := m.endingLocked(token, handle)
	return func() {
		handle.abort()
		end(outcome)
	}
}

// endingLocked drops a handle and records it as ending; m.mu is held. The
// returned function records how it ended, once it did.
func (m *TransactionManager) endingLocked(token string, handle *transactionHandle) func(outcome string) {
	m.removeLocked(token, handle)
	if handle.stopTimer != nil {
		handle.stopTimer()
	}
	if handle.stopIdle != nil {
		handle.stopIdle()
	}
	done := make(chan struct{})
	m.ending[token] = done
	return func(outcome string) {
		m.mu.Lock()
		delete(m.ending, token)
		m.buryLocked(token, handle, outcome)
		m.mu.Unlock()
		close(done)
	}
}

// abort rolls back the transaction unless it already ended and releases its
// lifetime context.
func (h *transactionHandle) abort() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.closed {
		h.closed = true
		_ = h.tx.Rollback()
	}
	h.cancel()
}

// lookup finds the handle of token for the caller. Given connections, the
// current ones of datasource, the transaction must run on one of them. A
// token still ending is waited for only as long as ctx lasts: its rollback
// waits on the database, which may be gone.
func (m *TransactionManager) lookup(
	ctx context.Context,
	token string,
	caller Caller,
	datasource string,
	connections []store.TxBeginner,
) (*transactionHandle, error) {
	m.mu.Lock()
	handle, ending := m.handles[token], m.ending[token]
	m.mu.Unlock()
	if handle == nil {
		if ending != nil {
			select {
			case <-ending:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return nil, m.endedError(token, caller)
	}
	if handle.session != caller.Session || handle.scope != caller.scope() ||
		datasource != "" && handle.datasource != datasource {
		return nil, ErrTransactionScope
	}
	if connections != nil && !slices.Contains(connections, handle.connection) {
		return nil, ErrTransactionStale
	}
	if !handle.expires.IsZero() && time.Now().After(handle.expires) {
		m.expire(token, handle)
		return nil, &TransactionEndedError{Outcome: OutcomeExpired}
	}
	return handle, nil
}

func (m *TransactionManager) DB(
	ctx context.Context,
	token string,
	caller Caller,
	datasource string,
	connections []store.TxBeginner,
) (store.DB, error) {
	handle, err := m.lookup(ctx, token, caller, datasource, connections)
	if err != nil {
		return nil, err
	}
	return &transactionDB{handle: handle, fail: func() { m.end(token, handle, OutcomeRolledBack) }}, nil
}

// Validate verifies a token's transport and authorization identity without
// exposing its transaction or datasource.
func (m *TransactionManager) Validate(ctx context.Context, token string, caller Caller) error {
	_, err := m.lookup(ctx, token, caller, "", nil)
	return err
}

// Configuration returns the limits in force.
func (m *TransactionManager) Configuration() (time.Duration, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ttl, m.maxOpen
}

func (m *TransactionManager) finish(
	ctx context.Context,
	token string,
	caller Caller,
	commit bool,
) ([]CacheTarget, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for {
		handle, err := m.lookup(ctx, token, caller, "", nil)
		if err != nil {
			return nil, err
		}
		m.mu.Lock()
		if m.handles[token] != handle {
			// Ending meanwhile: looked up again, its outcome is waited for.
			m.mu.Unlock()
			continue
		}
		end := m.endingLocked(token, handle)
		m.mu.Unlock()
		outcome, targets, err := handle.finish(ctx, commit)
		end(outcome)
		return targets, err
	}
}

// finish commits or rolls back the transaction, and reports how it ended. A
// commit that may have committed (see uncertain) returns what the
// transaction wrote with ErrOutcomeUnknown, for the caller to follow.
func (h *transactionHandle) finish(ctx context.Context, commit bool) (string, []CacheTarget, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	defer h.cancel()
	if h.closed {
		return OutcomeRolledBack, nil, ErrTransactionNotFound
	}
	h.closed = true
	if err := ctx.Err(); err != nil {
		_ = h.tx.Rollback()
		return OutcomeRolledBack, nil, err
	}
	stopDeadline := context.AfterFunc(ctx, h.cancel)
	defer stopDeadline()
	if !commit {
		return OutcomeRolledBack, nil, WrapDBError(h.tx.Rollback())
	}
	targets := make([]CacheTarget, 0, len(h.dirty))
	for target := range h.dirty {
		targets = append(targets, target)
	}
	if err := h.tx.Commit(); err != nil {
		_ = h.tx.Rollback()
		if uncertain(err) {
			return OutcomeUnknown, targets, outcomeUnknown(err)
		}
		return OutcomeRolledBack, nil, WrapDBError(err)
	}
	return OutcomeCommitted, targets, nil
}

// Commit commits and returns the cache targets the transaction wrote. The list
// is returned only after a commit that succeeded, or may have (with an
// ErrOutcomeUnknown), so callers can invalidate global read caches without
// rollback pollution.
func (m *TransactionManager) Commit(ctx context.Context, token string, caller Caller) ([]CacheTarget, error) {
	return m.finish(ctx, token, caller, true)
}

// Rollback rolls back the transaction.
func (m *TransactionManager) Rollback(ctx context.Context, token string, caller Caller) error {
	_, err := m.finish(ctx, token, caller, false)
	return err
}

// MarkDirty records the cache targets a write inside a transaction changed;
// connections are as for DB.
func (m *TransactionManager) MarkDirty(
	ctx context.Context,
	token string,
	caller Caller,
	datasource string,
	connections []store.TxBeginner,
	targets ...CacheTarget,
) error {
	handle, err := m.lookup(ctx, token, caller, datasource, connections)
	if err != nil {
		return err
	}
	handle.mu.Lock()
	defer handle.mu.Unlock()
	if handle.closed {
		return ErrTransactionNotFound
	}
	for _, target := range targets {
		handle.dirty[target] = struct{}{}
	}
	return nil
}

// RollbackSession rolls back all handles owned by a disconnected non-empty
// transport session. Token-only transports use TTL and App.Close instead.
func (m *TransactionManager) RollbackSession(session string) {
	if session == "" {
		return
	}
	m.mu.Lock()
	var rollbacks []func()
	for token, handle := range m.handles {
		if handle.session == session {
			rollbacks = append(rollbacks, m.endLocked(token, handle, OutcomeRolledBack))
		}
	}
	m.mu.Unlock()
	for _, rollback := range rollbacks {
		rollback()
	}
}

func (m *TransactionManager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	rollbacks := make([]func(), 0, len(m.handles))
	for token, handle := range m.handles {
		rollbacks = append(rollbacks, m.endLocked(token, handle, OutcomeRolledBack))
	}
	m.mu.Unlock()
	for _, rollback := range rollbacks {
		rollback()
	}
}

// transactionDB runs statements in a transaction; one that fails ends it
// (fail), and its error says so (statementFailed).
type transactionDB struct {
	handle *transactionHandle
	fail   func()
}

func (d *transactionDB) QueryContext(ctx context.Context, query string, args ...any) (store.Rows, error) {
	d.handle.mu.Lock()
	if d.handle.closed {
		d.handle.mu.Unlock()
		return nil, ErrTransactionNotFound
	}
	d.handle.statementStarted()
	rows, err := d.handle.tx.QueryContext(ctx, query, args...)
	if err != nil {
		d.handle.statementEnded()
		d.handle.mu.Unlock()
		d.fail()
		return nil, &statementFailed{err: err}
	}
	return &transactionRows{Rows: rows, db: d}, nil
}

func (d *transactionDB) ExecContext(ctx context.Context, query string, args ...any) (store.Result, error) {
	d.handle.mu.Lock()
	if d.handle.closed {
		d.handle.mu.Unlock()
		return store.Result{}, ErrTransactionNotFound
	}
	d.handle.statementStarted()
	r, err := d.handle.tx.ExecContext(ctx, query, args...)
	d.handle.statementEnded()
	d.handle.mu.Unlock()
	if err != nil {
		d.fail()
		return r, &statementFailed{err: err}
	}
	return r, nil
}

// transactionRows hold the transaction until closed; a failure reading them
// ends it once they are.
type transactionRows struct {
	store.Rows
	db     *transactionDB
	once   sync.Once
	failed bool
	err    error
}

func (r *transactionRows) Err() error {
	if err := r.Rows.Err(); err != nil {
		r.failed = true
		return &statementFailed{err: err}
	}
	return nil
}

func (r *transactionRows) Close() error {
	r.once.Do(func() {
		r.err = r.Rows.Close()
		r.db.handle.statementEnded()
		r.db.handle.mu.Unlock()
		if r.failed || r.err != nil {
			r.db.fail()
		}
	})
	return r.err
}
