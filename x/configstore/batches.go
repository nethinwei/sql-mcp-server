package configstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nethinwei/sql-mcp-server/x/catalog"
)

// Scan batches are kept with the catalog, so a batch goes on after a restart
// and every replica sees its progress (see catalog.BatchStore). A batch is a
// row and each of its schemas another: the owner moves one schema at a time,
// in a transaction that first writes the batch row (its heartbeat) on the
// condition that it still owns it, which locks the row against a takeover
// until the move commits.

const (
	batchColumns = "id, datasource, started_by, request_key, owner, started_us, finished_us, heartbeat_us, canceled"
	itemColumns  = "schema_name, state, error_text, attempts, not_before_us"
	// itemsPerInsert bounds the rows of one INSERT.
	itemsPerInsert = 200
)

func scanBatch(row scanner) (catalog.Batch, error) {
	var b catalog.Batch
	var started, finished, beat int64
	var canceled int
	if err := row.Scan(&b.ID, &b.Datasource, &b.StartedBy, &b.RequestKey, &b.Owner, &started, &finished, &beat,
		&canceled); err != nil {
		return catalog.Batch{}, err
	}
	b.StartedAt, b.FinishedAt, b.Heartbeat, b.Canceled = fromMicros(started), fromMicros(finished), fromMicros(beat),
		canceled != 0
	return b, nil
}

// withItems reads the items of each batch.
func (s *SQLStore) withItems(ctx context.Context, q execer, batches []catalog.Batch) error {
	for i := range batches {
		rows, err := q.QueryContext(ctx, s.dialect.rebind(
			"SELECT "+itemColumns+" FROM smcp_scan_batch_items WHERE batch_id = ? ORDER BY ordinal"), batches[i].ID)
		if err != nil {
			return fmt.Errorf("configstore: read scan batch items: %w", err)
		}
		batches[i].Items = []catalog.BatchItem{}
		for rows.Next() {
			var it catalog.BatchItem
			var notBefore int64
			if err := rows.Scan(&it.Schema, &it.State, &it.Error, &it.Attempts, &notBefore); err != nil {
				_ = rows.Close()
				return err
			}
			it.NotBefore = fromMicros(notBefore)
			batches[i].Items = append(batches[i].Items, it)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLStore) batches(ctx context.Context, query string, args ...any) ([]catalog.Batch, error) {
	rows, err := s.db.QueryContext(ctx, s.dialect.rebind(query), args...)
	if err != nil {
		return nil, fmt.Errorf("configstore: read scan batches: %w", err)
	}
	out := []catalog.Batch{}
	for rows.Next() {
		b, err := scanBatch(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, b)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	return out, s.withItems(ctx, s.db, out)
}

// CreateBatch implements catalog.BatchStore: the checks and the inserts run
// in one transaction under the store lock, serializing creations across
// processes.
func (s *SQLStore) CreateBatch(ctx context.Context, b catalog.Batch, maxOpen int) (catalog.Batch, bool, error) {
	var existing *catalog.Batch
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if err := s.lock(ctx, tx); err != nil {
			return err
		}
		if b.RequestKey != "" {
			found, err := scanBatch(s.queryRow(ctx, tx, "SELECT "+batchColumns+
				" FROM smcp_scan_batches WHERE datasource = ? AND request_key = ?", b.Datasource, b.RequestKey))
			if err == nil {
				existing = &found
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		var open int
		if err := s.queryRow(ctx, tx, "SELECT count(*) FROM smcp_scan_batches WHERE datasource = ? AND finished_us = 0",
			b.Datasource).Scan(&open); err != nil {
			return err
		}
		if open >= maxOpen {
			return catalog.ErrTooManyBatches
		}
		// payload held the items before schema version 4; it stays empty.
		if _, err := s.exec(ctx, tx, "INSERT INTO smcp_scan_batches ("+batchColumns+
			", payload) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '[]')", b.ID, b.Datasource, b.StartedBy, b.RequestKey, b.Owner,
			toMicros(b.StartedAt), toMicros(b.FinishedAt), toMicros(b.Heartbeat), boolInt(b.Canceled)); err != nil {
			return err
		}
		return s.insertItems(ctx, tx, b.ID, b.Items)
	})
	if err != nil {
		return catalog.Batch{}, false, err
	}
	if existing != nil {
		batches := []catalog.Batch{*existing}
		return batches[0], false, s.withItems(ctx, s.db, batches)
	}
	return b, true, nil
}

func (s *SQLStore) insertItems(ctx context.Context, tx *sql.Tx, id string, items []catalog.BatchItem) error {
	for start := 0; start < len(items); start += itemsPerInsert {
		chunk := items[start:min(start+itemsPerInsert, len(items))]
		var args []any
		for i, it := range chunk {
			args = append(args, id, start+i, it.Schema, it.State, it.Error, it.Attempts, toMicros(it.NotBefore))
		}
		values := strings.TrimSuffix(strings.Repeat("(?, ?, ?, ?, ?, ?, ?), ", len(chunk)), ", ")
		if _, err := s.exec(ctx, tx, "INSERT INTO smcp_scan_batch_items (batch_id, ordinal, "+itemColumns+
			") VALUES "+values, args...); err != nil {
			return err
		}
	}
	return nil
}

// Batch implements catalog.BatchStore.
func (s *SQLStore) Batch(ctx context.Context, id string) (catalog.Batch, bool, error) {
	all, err := s.batches(ctx, "SELECT "+batchColumns+" FROM smcp_scan_batches WHERE id = ?", id)
	if err != nil || len(all) == 0 {
		return catalog.Batch{}, false, err
	}
	return all[0], true, nil
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

// beatOwned writes the heartbeat of a batch owner still owns, unfinished,
// locking its row for the rest of tx; ErrNotOwner otherwise. The heartbeat
// increases from write to write (the owner's clock does), so the update
// changes the row and counts as affected on MySQL too.
func (s *SQLStore) beatOwned(ctx context.Context, tx *sql.Tx, id, owner string, now time.Time) error {
	res, err := s.exec(ctx, tx, "UPDATE smcp_scan_batches SET heartbeat_us = ? WHERE id = ? AND owner = ? "+
		"AND finished_us = 0", toMicros(now), id, owner)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n != 1 {
		err = catalog.ErrNotOwner
	}
	return err
}

// MoveItem implements catalog.BatchStore.
func (s *SQLStore) MoveItem(
	ctx context.Context, id, owner string, now time.Time, item catalog.BatchItem, from ...string,
) (bool, error) {
	if len(from) == 0 {
		return false, errors.New("configstore: move a scan batch item from some state")
	}
	moved := false
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if err := s.beatOwned(ctx, tx, id, owner, now); err != nil {
			return err
		}
		args := []any{item.State, item.Error, item.Attempts, toMicros(item.NotBefore), id, item.Schema}
		for _, st := range from {
			args = append(args, st)
		}
		res, err := s.exec(ctx, tx, "UPDATE smcp_scan_batch_items SET state = ?, error_text = ?, attempts = ?, "+
			"not_before_us = ? WHERE batch_id = ? AND schema_name = ? AND state IN ("+
			strings.TrimSuffix(strings.Repeat("?, ", len(from)), ", ")+")", args...)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		moved = n == 1
		return err
	})
	if err != nil && !errors.Is(err, catalog.ErrNotOwner) {
		err = fmt.Errorf("configstore: move scan batch item: %w", err)
	}
	return moved, err
}

// FinishBatch implements catalog.BatchStore.
func (s *SQLStore) FinishBatch(ctx context.Context, id, owner string, at time.Time) error {
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if err := s.beatOwned(ctx, tx, id, owner, at); err != nil {
			return err
		}
		if _, err := s.exec(ctx, tx, "UPDATE smcp_scan_batches SET finished_us = ? WHERE id = ?", toMicros(at),
			id); err != nil {
			return err
		}
		_, err := s.exec(ctx, tx, "UPDATE smcp_scan_batch_items SET state = ? WHERE batch_id = ? AND state IN (?, ?)",
			catalog.ItemCanceled, id, catalog.ItemPending, catalog.ItemRunning)
		return err
	})
	if err != nil && !errors.Is(err, catalog.ErrNotOwner) {
		err = fmt.Errorf("configstore: finish scan batch: %w", err)
	}
	return err
}

// CancelBatch implements catalog.BatchStore.
func (s *SQLStore) CancelBatch(ctx context.Context, id string) error {
	_, err := s.exec(ctx, s.db, "UPDATE smcp_scan_batches SET canceled = 1 WHERE id = ?", id)
	return err
}

// dataMigrations move data when a schema version needs more than DDL; each
// runs in the transaction of its version, after its DDL.
var dataMigrations = map[int64]func(ctx context.Context, s *SQLStore, tx *sql.Tx) error{
	4: migrateBatchItems,
	5: func(ctx context.Context, s *SQLStore, tx *sql.Tx) error {
		_, err := s.exec(ctx, tx, "INSERT INTO smcp_store_meta (meta_key, meta_value) VALUES (?, 0)", metaRevisionSeq)
		return err
	},
}

// migrateBatchItems moves the items of the batches kept before schema
// version 4, a JSON array in their payload, to rows; a schema being scanned
// stays running, for whoever takes the batch over to scan again.
func migrateBatchItems(ctx context.Context, s *SQLStore, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "SELECT id, payload FROM smcp_scan_batches")
	if err != nil {
		return err
	}
	payloads := map[string]string{}
	for rows.Next() {
		var id, payload string
		if err := rows.Scan(&id, &payload); err != nil {
			_ = rows.Close()
			return err
		}
		payloads[id] = payload
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for id, payload := range payloads {
		var old []struct{ Schema, State, Error string }
		if err := json.Unmarshal([]byte(payload), &old); err != nil {
			return fmt.Errorf("scan batch %q: %w", id, err)
		}
		items := make([]catalog.BatchItem, 0, len(old))
		for _, it := range old {
			items = append(items, catalog.BatchItem{Schema: it.Schema, State: it.State, Error: it.Error})
		}
		if err := s.insertItems(ctx, tx, id, items); err != nil {
			return err
		}
		if _, err := s.exec(ctx, tx, "UPDATE smcp_scan_batches SET payload = '[]' WHERE id = ?", id); err != nil {
			return err
		}
	}
	return nil
}
