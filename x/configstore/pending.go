package configstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// PendingState is how administrators handle one pending item (a recurring
// template's proposal, a table whose fields differ, ...): open, snoozed until
// a time, or ignored for a reason, and who owns it. Every change is logged.
type PendingState struct {
	Key       string
	Status    string
	Owner     string
	Reason    string
	Until     time.Time
	UpdatedBy string
	UpdatedAt time.Time
}

const pendingColumns = "item_key, status, owner, reason, until_us, updated_by, updated_us"

func scanPending(row scanner) (PendingState, error) {
	var p PendingState
	var until, updated int64
	if err := row.Scan(&p.Key, &p.Status, &p.Owner, &p.Reason, &until, &p.UpdatedBy, &updated); err != nil {
		return PendingState{}, err
	}
	p.Until, p.UpdatedAt = fromMicros(until), fromMicros(updated)
	return p, nil
}

func (s *SQLStore) pendingStates(ctx context.Context, query string, args ...any) ([]PendingState, error) {
	rows, err := s.db.QueryContext(ctx, s.dialect.rebind(query), args...)
	if err != nil {
		return nil, fmt.Errorf("configstore: read pending states: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []PendingState{}
	for rows.Next() {
		p, err := scanPending(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PendingStates returns the state of every item handled so far.
func (s *SQLStore) PendingStates(ctx context.Context) ([]PendingState, error) {
	return s.pendingStates(ctx, "SELECT "+pendingColumns+" FROM smcp_pending_states ORDER BY item_key")
}

// PendingLog returns the changes of an item's state, newest first.
func (s *SQLStore) PendingLog(ctx context.Context, key string) ([]PendingState, error) {
	return s.pendingStates(ctx, "SELECT "+pendingColumns+
		" FROM smcp_pending_log WHERE item_key = ? ORDER BY updated_us DESC", key)
}

// PutPendingState sets an item's state and logs the change.
func (s *SQLStore) PutPendingState(ctx context.Context, p PendingState) (PendingState, error) {
	p.UpdatedAt = s.timestamp()
	args := []any{p.Key, p.Status, p.Owner, p.Reason, toMicros(p.Until), p.UpdatedBy, toMicros(p.UpdatedAt)}
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := s.exec(ctx, tx, "DELETE FROM smcp_pending_states WHERE item_key = ?", p.Key); err != nil {
			return err
		}
		if _, err := s.exec(ctx, tx, "INSERT INTO smcp_pending_states ("+pendingColumns+
			") VALUES (?, ?, ?, ?, ?, ?, ?)", args...); err != nil {
			return err
		}
		_, err := s.exec(ctx, tx, "INSERT INTO smcp_pending_log ("+pendingColumns+
			") VALUES (?, ?, ?, ?, ?, ?, ?)", args...)
		return err
	})
	if err != nil {
		return PendingState{}, fmt.Errorf("configstore: save pending state: %w", err)
	}
	return p, nil
}
