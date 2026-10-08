package configstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/revision"
)

// SQLStore is a revision.Store backed by database/sql. IDs come from a
// counter row and every publish or rollback first updates a lock row, so all
// state transitions serialize on a row lock in every supported backend.
type SQLStore struct {
	db      *sql.DB
	dialect dialect
	now     func() time.Time
}

func defaultNow() time.Time { return time.Now() }

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *SQLStore) exec(ctx context.Context, q execer, query string, args ...any) (sql.Result, error) {
	return q.ExecContext(ctx, s.dialect.rebind(query), args...)
}

func (s *SQLStore) queryRow(ctx context.Context, q execer, query string, args ...any) *sql.Row {
	return q.QueryRowContext(ctx, s.dialect.rebind(query), args...)
}

const revisionColumns = "id, parent_id, content_hash, payload, state, author, comment_text, created_us, published_us"

const summaryColumns = "id, parent_id, content_hash, '' AS payload, state, author, comment_text, " +
	"created_us, published_us"

type scanner interface{ Scan(dest ...any) error }

func scanRevision(row scanner) (revision.Revision, error) {
	var r revision.Revision
	var payload, state string
	var created, published int64
	if err := row.Scan(&r.ID, &r.ParentID, &r.ContentHash, &payload, &state, &r.Author, &r.Comment,
		&created, &published); err != nil {
		return revision.Revision{}, err
	}
	if payload != "" {
		r.Payload = []byte(payload)
	}
	r.State = revision.State(state)
	r.CreatedAt = fromMicros(created)
	r.PublishedAt = fromMicros(published)
	return r, nil
}

func toMicros(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UTC().UnixMicro()
}

func fromMicros(us int64) time.Time {
	if us == 0 {
		return time.Time{}
	}
	return time.UnixMicro(us).UTC()
}

// withTx runs fn in a transaction, rolling back on error.
func (s *SQLStore) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// lock serializes writers on the publish lock row.
func (s *SQLStore) lock(ctx context.Context, tx *sql.Tx) error {
	_, err := s.exec(ctx, tx, "UPDATE smcp_store_meta SET meta_value = meta_value + 1 WHERE meta_key = ?",
		metaPublishLock)
	return err
}

func (s *SQLStore) allocateID(ctx context.Context, tx *sql.Tx) (int64, error) {
	if _, err := s.exec(ctx, tx, "UPDATE smcp_store_meta SET meta_value = meta_value + 1 WHERE meta_key = ?",
		metaNextID); err != nil {
		return 0, err
	}
	var next int64
	if err := s.queryRow(ctx, tx, "SELECT meta_value FROM smcp_store_meta WHERE meta_key = ?",
		metaNextID).Scan(&next); err != nil {
		return 0, err
	}
	return next - 1, nil
}

func (s *SQLStore) insert(ctx context.Context, tx *sql.Tx, r revision.Revision) error {
	_, err := s.exec(ctx, tx, "INSERT INTO smcp_revisions ("+revisionColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		r.ID, r.ParentID, r.ContentHash, string(r.Payload), string(r.State), r.Author, r.Comment,
		toMicros(r.CreatedAt), toMicros(r.PublishedAt))
	return err
}

func (s *SQLStore) timestamp() time.Time {
	return s.now().UTC().Truncate(time.Microsecond)
}

// Create implements revision.Store.
func (s *SQLStore) Create(ctx context.Context, d revision.Draft) (revision.Revision, error) {
	r := revision.Revision{
		ParentID: d.ParentID, ContentHash: revision.Hash(d.Payload), Payload: d.Payload,
		State: revision.StateDraft, Author: d.Author, Comment: d.Comment, CreatedAt: s.timestamp(),
	}
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		id, err := s.allocateID(ctx, tx)
		if err != nil {
			return err
		}
		r.ID = id
		return s.insert(ctx, tx, r)
	})
	if err != nil {
		return revision.Revision{}, fmt.Errorf("configstore: create revision: %w", err)
	}
	return r, nil
}

// Get implements revision.Store.
func (s *SQLStore) Get(ctx context.Context, id int64) (revision.Revision, error) {
	return s.get(ctx, s.db, id)
}

func (s *SQLStore) get(ctx context.Context, q execer, id int64) (revision.Revision, error) {
	r, err := scanRevision(s.queryRow(ctx, q, "SELECT "+revisionColumns+" FROM smcp_revisions WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return revision.Revision{}, fmt.Errorf("%w: revision %d", revision.ErrNotFound, id)
	}
	return r, err
}

// List implements revision.Store.
func (s *SQLStore) List(ctx context.Context, limit int) ([]revision.Revision, error) {
	return s.list(ctx, s.db, limit)
}

func (s *SQLStore) list(ctx context.Context, q execer, limit int) ([]revision.Revision, error) {
	query := "SELECT " + summaryColumns + " FROM smcp_revisions ORDER BY id DESC"
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := q.QueryContext(ctx, s.dialect.rebind(query))
	if err != nil {
		return nil, fmt.Errorf("configstore: list revisions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []revision.Revision
	for rows.Next() {
		r, err := scanRevision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Published implements revision.Store.
func (s *SQLStore) Published(ctx context.Context) (revision.Revision, error) {
	return s.published(ctx, s.db)
}

func (s *SQLStore) published(ctx context.Context, q execer) (revision.Revision, error) {
	r, err := scanRevision(s.queryRow(ctx, q, "SELECT "+revisionColumns+" FROM smcp_revisions WHERE state = ?",
		string(revision.StatePublished)))
	if errors.Is(err, sql.ErrNoRows) {
		return revision.Revision{}, revision.ErrNoPublished
	}
	return r, err
}

// currentLocked returns the published revision after checking it matches the
// caller's expectation; it must run after lock.
func (s *SQLStore) currentLocked(ctx context.Context, tx *sql.Tx, expected int64) (revision.Revision, error) {
	current, err := s.published(ctx, tx)
	if err != nil && !errors.Is(err, revision.ErrNoPublished) {
		return revision.Revision{}, err
	}
	if current.ID != expected {
		return revision.Revision{}, fmt.Errorf("%w: expected %d, found %d", revision.ErrConflict, expected, current.ID)
	}
	return current, nil
}

func (s *SQLStore) setState(
	ctx context.Context, tx *sql.Tx, id int64, state revision.State, publishedAt time.Time,
) error {
	query := "UPDATE smcp_revisions SET state = ? WHERE id = ?"
	args := []any{string(state), id}
	if !publishedAt.IsZero() {
		query = "UPDATE smcp_revisions SET state = ?, published_us = ? WHERE id = ?"
		args = []any{string(state), toMicros(publishedAt), id}
	}
	_, err := s.exec(ctx, tx, query, args...)
	return err
}

// Publish implements revision.Store.
func (s *SQLStore) Publish(ctx context.Context, id, expectedCurrent int64, _ revision.Meta) (revision.Revision, error) {
	var out revision.Revision
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if err := s.lock(ctx, tx); err != nil {
			return err
		}
		target, err := s.get(ctx, tx, id)
		if err != nil {
			return err
		}
		if target.State != revision.StateDraft {
			return fmt.Errorf("%w: revision %d is %s, only drafts can be published",
				revision.ErrInvalidState, id, target.State)
		}
		current, err := s.currentLocked(ctx, tx, expectedCurrent)
		if err != nil {
			return err
		}
		if current.ID != 0 {
			if err := s.setState(ctx, tx, current.ID, revision.StateSuperseded, time.Time{}); err != nil {
				return err
			}
		}
		target.State, target.PublishedAt = revision.StatePublished, s.timestamp()
		out = target
		return s.setState(ctx, tx, id, target.State, target.PublishedAt)
	})
	if err != nil {
		return revision.Revision{}, err
	}
	return out, nil
}

// Rollback implements revision.Store.
func (s *SQLStore) Rollback(
	ctx context.Context, expectedCurrent, target int64, m revision.Meta,
) (revision.Revision, error) {
	if expectedCurrent == 0 {
		return revision.Revision{}, revision.ErrNoPublished
	}
	var out revision.Revision
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if err := s.lock(ctx, tx); err != nil {
			return err
		}
		current, err := s.currentLocked(ctx, tx, expectedCurrent)
		if err != nil {
			return err
		}
		all, err := s.list(ctx, tx, 0)
		if err != nil {
			return err
		}
		chosen, err := revision.RollbackTarget(all, current, target)
		if err != nil {
			return err
		}
		source, err := s.get(ctx, tx, chosen.ID)
		if err != nil {
			return err
		}
		if err := s.setState(ctx, tx, current.ID, revision.StateRolledBack, time.Time{}); err != nil {
			return err
		}
		id, err := s.allocateID(ctx, tx)
		if err != nil {
			return err
		}
		now := s.timestamp()
		out = revision.Revision{
			ID: id, ParentID: source.ID, ContentHash: source.ContentHash, Payload: source.Payload,
			State: revision.StatePublished, Author: m.Author, Comment: m.Comment, CreatedAt: now, PublishedAt: now,
		}
		return s.insert(ctx, tx, out)
	})
	if err != nil {
		return revision.Revision{}, err
	}
	return out, nil
}

// ImportHistory implements revision.Store.
func (s *SQLStore) ImportHistory(ctx context.Context, revs []revision.Revision) error {
	if err := revision.ValidateHistory(revs); err != nil {
		return err
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if err := s.lock(ctx, tx); err != nil {
			return err
		}
		var count int64
		if err := s.queryRow(ctx, tx, "SELECT COUNT(*) FROM smcp_revisions").Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return revision.ErrNotEmpty
		}
		var maxID int64
		for _, r := range revs {
			if err := s.insert(ctx, tx, r); err != nil {
				return err
			}
			maxID = max(maxID, r.ID)
		}
		_, err := s.exec(ctx, tx, "UPDATE smcp_store_meta SET meta_value = ? WHERE meta_key = ?", maxID+1, metaNextID)
		return err
	})
}

// Close implements revision.Store.
func (s *SQLStore) Close() error { return s.db.Close() }
