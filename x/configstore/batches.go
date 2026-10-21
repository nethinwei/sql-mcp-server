package configstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nethinwei/sql-mcp-server/internal/kit"
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

// batchFields are where batchColumns scan into; done fills b from them.
func batchFields(b *catalog.Batch) (dest []any, done func()) {
	var started, finished, beat int64
	var canceled int
	return []any{&b.ID, &b.Datasource, &b.StartedBy, &b.RequestKey, &b.Owner, &started, &finished, &beat, &canceled},
		func() {
			b.StartedAt, b.FinishedAt, b.Heartbeat = fromMicros(started), fromMicros(finished), fromMicros(beat)
			b.Canceled = canceled != 0
		}
}

func scanBatch(row scanner) (catalog.Batch, error) {
	var b catalog.Batch
	dest, done := batchFields(&b)
	if err := row.Scan(dest...); err != nil {
		return catalog.Batch{}, err
	}
	done()
	return b, nil
}

// batchRow is a batch with one of its items, if it has any.
type batchRow struct {
	batch catalog.Batch
	item  *catalog.BatchItem
}

func scanBatchRow(row scanner) (batchRow, error) {
	var r batchRow
	var schema, state, errText sql.NullString
	var attempts, notBefore sql.NullInt64
	dest, done := batchFields(&r.batch)
	if err := row.Scan(append(dest, &schema, &state, &errText, &attempts, &notBefore)...); err != nil {
		return batchRow{}, err
	}
	done()
	if schema.Valid {
		r.item = &catalog.BatchItem{Schema: schema.String, State: state.String, Error: errText.String,
			Attempts: int(attempts.Int64), NotBefore: fromMicros(notBefore.Int64)}
	}
	return r, nil
}

// batchItem is an item with the batch it is of.
type batchItem struct {
	batch string
	item  catalog.BatchItem
}

func scanBatchItem(row scanner) (batchItem, error) {
	var it batchItem
	var notBefore int64
	if err := row.Scan(&it.batch, &it.item.Schema, &it.item.State, &it.item.Error, &it.item.Attempts,
		&notBefore); err != nil {
		return batchItem{}, err
	}
	it.item.NotBefore = fromMicros(notBefore)
	return it, nil
}

// withItems reads the items of the batches, in one query.
func (s *SQLStore) withItems(ctx context.Context, q execer, batches []catalog.Batch) error {
	if len(batches) == 0 {
		return nil
	}
	ids := make([]any, len(batches))
	at := make(map[string]int, len(batches))
	for i, b := range batches {
		ids[i], at[b.ID] = b.ID, i
		batches[i].Items = []catalog.BatchItem{}
	}
	items, err := queryAll(ctx, s, q, scanBatchItem, "SELECT batch_id, "+itemColumns+
		" FROM smcp_scan_batch_items WHERE batch_id IN ("+placeholders(len(ids))+") ORDER BY batch_id, ordinal", ids...)
	if err != nil {
		return fmt.Errorf("configstore: read scan batch items: %w", err)
	}
	for _, it := range items {
		b := &batches[at[it.batch]]
		b.Items = append(b.Items, it.item)
	}
	return nil
}

// withCounts counts the items of the batches by state, in one query.
func (s *SQLStore) withCounts(ctx context.Context, batches []catalog.Batch) error {
	if len(batches) == 0 {
		return nil
	}
	ids := make([]any, len(batches))
	at := make(map[string]int, len(batches))
	for i, b := range batches {
		ids[i], at[b.ID] = b.ID, i
		batches[i].Counts = map[string]int{}
	}
	type count struct {
		batch, state string
		n            int
	}
	counts, err := queryAll(ctx, s, s.db, func(row scanner) (c count, err error) {
		return c, row.Scan(&c.batch, &c.state, &c.n)
	}, "SELECT batch_id, state, COUNT(*) FROM smcp_scan_batch_items WHERE batch_id IN ("+placeholders(len(ids))+
		") GROUP BY batch_id, state", ids...)
	if err != nil {
		return fmt.Errorf("configstore: count scan batch items: %w", err)
	}
	for _, c := range counts {
		batches[at[c.batch]].Counts[c.state] = c.n
	}
	return nil
}

// batches reads the batches query selects; with items, their items too,
// otherwise their counts.
func (s *SQLStore) batches(ctx context.Context, items bool, query string, args ...any) ([]catalog.Batch, error) {
	out, err := queryAll(ctx, s, s.db, scanBatch, query, args...)
	if err != nil {
		return nil, fmt.Errorf("configstore: read scan batches: %w", err)
	}
	out = kit.OrEmpty(out)
	if items {
		return out, s.withItems(ctx, s.db, out)
	}
	return out, s.withCounts(ctx, out)
}

// CreateBatch implements catalog.BatchStore: the checks and the inserts run
// in one transaction under the store lock, serializing creations across
// processes.
func (s *SQLStore) CreateBatch(ctx context.Context, b catalog.Batch, maxOpen int) (catalog.Batch, bool, error) {
	var existing *catalog.Batch
	err := s.locked(ctx, func(tx *sql.Tx) error {
		if err := s.dropOldBatches(ctx, tx, b.Datasource, b.StartedAt.Add(-catalog.BatchRetention)); err != nil {
			return err
		}
		if b.RequestKey != "" {
			found, err := scanBatch(s.queryRow(ctx, tx, "SELECT "+batchColumns+
				" FROM smcp_scan_batches WHERE datasource = ? AND request_key = ? AND started_by = ?",
				b.Datasource, b.RequestKey, b.StartedBy))
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
		if _, err := s.exec(ctx, tx, "INSERT INTO smcp_scan_batch_items (batch_id, ordinal, "+itemColumns+
			") VALUES "+tuples(len(chunk), 7), args...); err != nil {
			return err
		}
	}
	return nil
}

// Batch implements catalog.BatchStore.
func (s *SQLStore) Batch(ctx context.Context, id string) (catalog.Batch, bool, error) {
	all, err := s.batches(ctx, true, "SELECT "+batchColumns+" FROM smcp_scan_batches WHERE id = ?", id)
	if err != nil || len(all) == 0 {
		return catalog.Batch{}, false, err
	}
	return all[0], true, nil
}

// Batches implements catalog.BatchStore.
func (s *SQLStore) Batches(ctx context.Context, datasource string, limit int) ([]catalog.Batch, error) {
	return s.batches(ctx, false, fmt.Sprintf("SELECT "+batchColumns+
		" FROM smcp_scan_batches WHERE datasource = ? ORDER BY started_us DESC, id DESC LIMIT %d", max(limit, 0)), datasource)
}

// BatchesWithItems implements catalog.BatchStore in one statement: a batch
// pruned meanwhile is read whole or not at all.
func (s *SQLStore) BatchesWithItems(ctx context.Context, datasource string, limit int) ([]catalog.Batch, error) {
	rows, err := queryAll(ctx, s, s.db, scanBatchRow, fmt.Sprintf("SELECT "+batchColumns+", "+itemColumns+
		" FROM (SELECT "+batchColumns+" FROM smcp_scan_batches WHERE datasource = ?"+
		" ORDER BY started_us DESC, id DESC LIMIT %d) b LEFT JOIN smcp_scan_batch_items i ON i.batch_id = b.id"+
		" ORDER BY started_us DESC, id DESC, ordinal", max(limit, 0)), datasource)
	if err != nil {
		return nil, fmt.Errorf("configstore: read scan batches: %w", err)
	}
	out := []catalog.Batch{}
	for _, r := range rows {
		if len(out) == 0 || out[len(out)-1].ID != r.batch.ID {
			r.batch.Items = []catalog.BatchItem{}
			out = append(out, r.batch)
		}
		if r.item != nil {
			b := &out[len(out)-1]
			b.Items = append(b.Items, *r.item)
		}
	}
	return out, nil
}

// UnfinishedBatches implements catalog.BatchStore.
func (s *SQLStore) UnfinishedBatches(ctx context.Context) ([]catalog.Batch, error) {
	return s.batches(ctx, false, "SELECT "+batchColumns+" FROM smcp_scan_batches WHERE finished_us = 0 ORDER BY id")
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
			"not_before_us = ? WHERE batch_id = ? AND schema_name = ? AND state IN ("+placeholders(len(from))+")", args...)
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

// dropOldBatches drops the batches of datasource finished before, with their
// items.
func (s *SQLStore) dropOldBatches(ctx context.Context, tx *sql.Tx, datasource string, before time.Time) error {
	old := "SELECT id FROM smcp_scan_batches WHERE datasource = ? AND finished_us > 0 AND finished_us < ?"
	if _, err := s.exec(ctx, tx, "DELETE FROM smcp_scan_batch_items WHERE batch_id IN ("+old+")", datasource,
		toMicros(before)); err != nil {
		return err
	}
	_, err := s.exec(ctx, tx, "DELETE FROM smcp_scan_batches WHERE datasource = ? AND finished_us > 0 "+
		"AND finished_us < ?", datasource, toMicros(before))
	return err
}

// BatchCanceled implements catalog.BatchStore.
func (s *SQLStore) BatchCanceled(ctx context.Context, id string) (bool, error) {
	var canceled int
	err := s.queryRow(ctx, s.db, "SELECT canceled FROM smcp_scan_batches WHERE id = ?", id).Scan(&canceled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return canceled != 0, err
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
	type kept struct{ id, payload string }
	payloads, err := queryAll(ctx, s, tx, func(row scanner) (k kept, err error) {
		return k, row.Scan(&k.id, &k.payload)
	}, "SELECT id, payload FROM smcp_scan_batches")
	if err != nil {
		return err
	}
	for _, b := range payloads {
		id := b.id
		var old []struct{ Schema, State, Error string }
		if err := json.Unmarshal([]byte(b.payload), &old); err != nil {
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
