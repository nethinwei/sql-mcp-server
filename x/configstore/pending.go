package configstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/nethinwei/sql-mcp-server/internal/kit"
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
	out, err := queryAll(ctx, s, s.db, scanPending, query, args...)
	if err != nil {
		return nil, fmt.Errorf("configstore: read pending states: %w", err)
	}
	return kit.OrEmpty(out), nil
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

// PutPendingState sets an item's state and logs the change, in one
// transaction. Both writes are upserts: concurrent puts of one key (template
// keys are deterministic) each succeed, the last committed one is the state;
// two changes of a key in the same microsecond keep one log entry.
func (s *SQLStore) PutPendingState(ctx context.Context, p PendingState) (PendingState, error) {
	p.UpdatedAt = s.timestamp()
	args := []any{p.Key, p.Status, p.Owner, p.Reason, toMicros(p.Until), p.UpdatedBy, toMicros(p.UpdatedAt)}
	values := ") VALUES (?, ?, ?, ?, ?, ?, ?)"
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := s.exec(ctx, tx, "INSERT INTO smcp_pending_states ("+pendingColumns+values+
			s.dialect.upsert([]string{"item_key"}, "status", "owner", "reason", "until_us", "updated_by", "updated_us"),
			args...); err != nil {
			return err
		}
		_, err := s.exec(ctx, tx, "INSERT INTO smcp_pending_log ("+pendingColumns+values+
			s.dialect.upsert([]string{"item_key", "updated_us"}, "status", "owner", "reason", "until_us", "updated_by"),
			args...)
		return err
	})
	if err != nil {
		return PendingState{}, fmt.Errorf("configstore: save pending state: %w", err)
	}
	return p, nil
}
