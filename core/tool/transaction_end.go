package tool

import (
	"errors"
	"time"
)

// A statement that fails inside an explicit transaction ends it: the
// transaction is rolled back at once, releasing its connection and its locks.
// PostgreSQL refuses every later statement of a transaction one failed in, so
// keeping it open only held the locks until it expired; every database ends
// it alike, so the agent learns one rule.

// TransactionRolledBackError is the error of a statement whose failure
// rolled its transaction back; its token is then not found (outcome
// rolledBack).
type TransactionRolledBackError struct{ Err error }

func (e *TransactionRolledBackError) Error() string { return e.Err.Error() }
func (e *TransactionRolledBackError) Unwrap() error { return e.Err }

// statementFailed marks the database error of a statement that ran inside a
// transaction, which ended it.
type statementFailed struct{ err error }

func (e *statementFailed) Error() string { return e.err.Error() }
func (e *statementFailed) Unwrap() error { return e.err }

// endedTransaction reports whether err is a statement's that ended its
// transaction.
func endedTransaction(err error) bool {
	var failed *statementFailed
	return errors.As(err, &failed)
}

// end drops a handle still open and rolls it back, recording outcome.
func (m *TransactionManager) end(token string, handle *transactionHandle, outcome string) {
	m.mu.Lock()
	if m.handles[token] != handle {
		m.mu.Unlock()
		return
	}
	rollback := m.endLocked(token, handle, outcome)
	m.mu.Unlock()
	rollback()
}

// A transaction no statement used for its idle timeout is rolled back
// (outcome expired): an agent thinking between calls must not hold locks
// that hold up schema changes and the queries queued behind them.

// statementStarted and statementEnded bracket a statement of the handle,
// which keeps it from being idle.
func (h *transactionHandle) statementStarted() { h.running.Add(1); h.touch() }
func (h *transactionHandle) statementEnded()   { h.touch(); h.running.Add(-1) }
func (h *transactionHandle) touch()            { h.used.Store(time.Now().UnixNano()) }

// watchIdleLocked checks the handle for idleness after d; m.mu is held.
func (m *TransactionManager) watchIdleLocked(token string, handle *transactionHandle, d time.Duration) {
	handle.stopIdle = m.bus.AfterFunc("transaction idle", d, func() { m.checkIdle(token, handle) })
}

// checkIdle ends the handle when no statement used it for its idle timeout,
// and otherwise checks again when it could be.
func (m *TransactionManager) checkIdle(token string, handle *transactionHandle) {
	m.mu.Lock()
	if m.handles[token] != handle {
		m.mu.Unlock()
		return
	}
	left := handle.idle
	if handle.running.Load() == 0 {
		left -= time.Since(time.Unix(0, handle.used.Load()))
	}
	if left > 0 {
		m.watchIdleLocked(token, handle, left)
		m.mu.Unlock()
		return
	}
	rollback := m.endLocked(token, handle, OutcomeExpired)
	m.mu.Unlock()
	rollback()
}
