package tool

import (
	"time"
)

// How an explicit transaction ended, as its tombstone records it.
const (
	OutcomeCommitted  = "committed"
	OutcomeRolledBack = "rolledBack"
	OutcomeExpired    = "expired"
	// OutcomeUnknown is a commit whose connection broke or whose deadline
	// passed while it ran: the transaction may or may not have committed.
	OutcomeUnknown = "unknown"
)

// TransactionEndedError is ErrTransactionNotFound for a token whose
// transaction ended recently, saying how: a caller whose commit timed out
// learns whether it is worth retrying. Only the caller the transaction was
// bound to learns it.
type TransactionEndedError struct{ Outcome string }

func (e *TransactionEndedError) Error() string {
	return ErrTransactionNotFound.Error() + ": it ended (" + e.Outcome + ")"
}

func (e *TransactionEndedError) Unwrap() error { return ErrTransactionNotFound }

// Tombstones are kept for tombstoneRetention, at most maxTombstones of them.
const (
	tombstoneRetention = 10 * time.Minute
	maxTombstones      = 4096
)

type tombstone struct {
	token, session, scope, outcome string
	at                             time.Time
}

// buryLocked records how the transaction of token ended; m.mu is held.
func (m *TransactionManager) buryLocked(token string, handle *transactionHandle, outcome string) {
	now := time.Now()
	m.tombstones[token] = tombstone{
		token: token, session: handle.session, scope: handle.scope, outcome: outcome, at: now,
	}
	m.buried = append(m.buried, token)
	for len(m.buried) > 0 {
		oldest := m.tombstones[m.buried[0]]
		if len(m.buried) <= maxTombstones && now.Sub(oldest.at) < tombstoneRetention {
			break
		}
		if oldest.token == m.buried[0] {
			delete(m.tombstones, oldest.token)
		}
		m.buried = m.buried[1:]
	}
}

// endedError is the error for a token with no open transaction: how it
// ended, when it ended recently for the same caller, otherwise not found.
func (m *TransactionManager) endedError(token string, caller Caller) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tombstones[token]
	if !ok || time.Since(t.at) >= tombstoneRetention || t.session != caller.Session || t.scope != caller.scope() {
		return ErrTransactionNotFound
	}
	return &TransactionEndedError{Outcome: t.outcome}
}
