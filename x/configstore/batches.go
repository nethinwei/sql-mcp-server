package configstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nethinwei/sql-mcp-server/x/catalog"
)

// Scan batches are kept with the catalog, so a batch goes on after a restart
// and every replica sees its progress (see catalog.BatchStore).

const batchColumns = "id, datasource, started_by, owner, started_us, finished_us, heartbeat_us, canceled, payload"

func scanBatch(row scanner) (catalog.Batch, error) {
	var b catalog.Batch
	var started, finished, beat int64
	var canceled int
	var payload string
	if err := row.Scan(&b.ID, &b.Datasource, &b.StartedBy, &b.Owner, &started, &finished, &beat, &canceled,
		&payload); err != nil {
		return catalog.Batch{}, err
	}
	b.StartedAt, b.FinishedAt, b.Heartbeat, b.Canceled = fromMicros(started), fromMicros(finished), fromMicros(beat),
		canceled != 0
	if err := json.Unmarshal([]byte(payload), &b.Items); err != nil {
		return catalog.Batch{}, fmt.Errorf("configstore: decode scan batch %q: %w", b.ID, err)
	}
	return b, nil
}

func (s *SQLStore) batches(ctx context.Context, query string, args ...any) ([]catalog.Batch, error) {
	rows, err := s.db.QueryContext(ctx, s.dialect.rebind(query), args...)
	if err != nil {
		return nil, fmt.Errorf("configstore: read scan batches: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []catalog.Batch{}
	for rows.Next() {
		b, err := scanBatch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// PutBatch implements catalog.BatchStore. A cancel recorded meanwhile is
// kept.
func (s *SQLStore) PutBatch(ctx context.Context, b catalog.Batch) error {
	payload, err := json.Marshal(b.Items)
	if err != nil {
		return err
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var canceled int
		err := s.queryRow(ctx, tx, "SELECT canceled FROM smcp_scan_batches WHERE id = ?", b.ID).Scan(&canceled)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := s.exec(ctx, tx, "DELETE FROM smcp_scan_batches WHERE id = ?", b.ID); err != nil {
			return err
		}
		_, err = s.exec(
			ctx,
			tx,
			"INSERT INTO smcp_scan_batches ("+batchColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
			b.ID,
			b.Datasource,
			b.StartedBy,
			b.Owner,
			toMicros(b.StartedAt),
			toMicros(b.FinishedAt),
			toMicros(b.Heartbeat),
			boolInt(b.Canceled || canceled != 0),
			string(payload),
		)
		return err
	})
}

// SaveBatch implements catalog.BatchStore with one conditional update.
func (s *SQLStore) SaveBatch(ctx context.Context, b catalog.Batch) (bool, error) {
	payload, err := json.Marshal(b.Items)
	if err != nil {
		return false, err
	}
	res, err := s.exec(ctx, s.db, "UPDATE smcp_scan_batches SET payload = ?, heartbeat_us = ?, finished_us = ? "+
		"WHERE id = ? AND owner = ? AND finished_us = 0",
		string(payload), toMicros(b.Heartbeat), toMicros(b.FinishedAt), b.ID, b.Owner)
	if err != nil {
		return false, fmt.Errorf("configstore: save scan batch: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 1 {
		return err == nil, err
	}
	// MySQL counts changed rows: a write of what is already there changes
	// none, and still is the owner's.
	var owned int
	err = s.queryRow(ctx, s.db, "SELECT count(*) FROM smcp_scan_batches WHERE id = ? AND owner = ? AND finished_us = 0",
		b.ID, b.Owner).Scan(&owned)
	return owned == 1, err
}

// Batch implements catalog.BatchStore.
func (s *SQLStore) Batch(ctx context.Context, id string) (catalog.Batch, bool, error) {
	b, err := scanBatch(s.queryRow(ctx, s.db, "SELECT "+batchColumns+" FROM smcp_scan_batches WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return catalog.Batch{}, false, nil
	}
	return b, err == nil, err
}

// Batches implements catalog.BatchStore.
func (s *SQLStore) Batches(ctx context.Context, datasource string, limit int) ([]catalog.Batch, error) {
	return s.batches(ctx, fmt.Sprintf("SELECT "+batchColumns+
		" FROM smcp_scan_batches WHERE datasource = ? ORDER BY started_us DESC, id DESC LIMIT %d", max(limit, 0)), datasource)
}

// UnfinishedBatches implements catalog.BatchStore.
func (s *SQLStore) UnfinishedBatches(ctx context.Context) ([]catalog.Batch, error) {
	return s.batches(ctx, "SELECT "+batchColumns+" FROM smcp_scan_batches WHERE finished_us = 0 ORDER BY id")
}

// ClaimBatch implements catalog.BatchStore with one conditional update.
func (s *SQLStore) ClaimBatch(ctx context.Context, id, owner string, staleBefore, now time.Time) (bool, error) {
	res, err := s.exec(ctx, s.db, "UPDATE smcp_scan_batches SET owner = ?, heartbeat_us = ? "+
		"WHERE id = ? AND finished_us = 0 AND (owner = ? OR heartbeat_us < ?)",
		owner, toMicros(now), id, owner, toMicros(staleBefore))
	if err != nil {
		return false, fmt.Errorf("configstore: claim scan batch: %w", err)
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// CancelBatch implements catalog.BatchStore.
func (s *SQLStore) CancelBatch(ctx context.Context, id string) error {
	_, err := s.exec(ctx, s.db, "UPDATE smcp_scan_batches SET canceled = 1 WHERE id = ?", id)
	return err
}
