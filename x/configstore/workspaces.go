package configstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nethinwei/sql-mcp-server/core/workspace"
)

// Administrators' workspaces (core/workspace.Store): the items and the
// review state (conflicts, notes, drafts taken in, the last checkpoint) are
// JSON; each write is logged in smcp_workspace_log in the same transaction.

const workspaceColumns = "username, base_id, items, review, version, updated_us, updated_by"

// review is the part of a workspace kept as one JSON value.
type review struct {
	Conflicts    []workspace.Conflict `json:"conflicts,omitempty"`
	Notes        []workspace.Note     `json:"notes,omitempty"`
	Taken        []int64              `json:"taken,omitempty"`
	Checkpoint   int64                `json:"checkpoint,omitempty"`
	CheckpointAt int64                `json:"checkpointAt,omitempty"`
}

// Workspace implements workspace.Store.
func (s *SQLStore) Workspace(ctx context.Context, username string) (workspace.Workspace, bool, error) {
	return s.workspace(ctx, s.db, username)
}

func (s *SQLStore) workspace(ctx context.Context, q execer, username string) (workspace.Workspace, bool, error) {
	var w workspace.Workspace
	var items, rv string
	var updated int64
	err := s.queryRow(ctx, q, "SELECT "+workspaceColumns+" FROM smcp_workspaces WHERE username = ?", username).
		Scan(&w.Username, &w.Base, &items, &rv, &w.Version, &updated, &w.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return workspace.Workspace{}, false, nil
	}
	if err != nil {
		return workspace.Workspace{}, false, fmt.Errorf("configstore: read workspace: %w", err)
	}
	w.UpdatedAt = fromMicros(updated)
	if items != "" {
		if err := json.Unmarshal([]byte(items), &w.Items); err != nil {
			return workspace.Workspace{}, false, fmt.Errorf("configstore: decode the items of %q's workspace: %w", username, err)
		}
	}
	var r review
	if rv != "" {
		if err := json.Unmarshal([]byte(rv), &r); err != nil {
			return workspace.Workspace{}, false, fmt.Errorf("configstore: decode the review of %q's workspace: %w",
				username, err)
		}
	}
	w.Conflicts, w.Notes, w.Taken, w.Checkpoint, w.CheckpointAt = r.Conflicts, r.Notes, r.Taken, r.Checkpoint,
		r.CheckpointAt
	return w, true, nil
}

// PutWorkspace implements workspace.Store, checking and writing under the
// store lock.
func (s *SQLStore) PutWorkspace(
	ctx context.Context, w workspace.Workspace, expected int64, entry workspace.LogEntry,
) (workspace.Workspace, error) {
	items, rv, err := encodeWorkspace(w)
	if err != nil {
		return workspace.Workspace{}, err
	}
	w.UpdatedAt = s.timestamp()
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if err := s.lock(ctx, tx); err != nil {
			return err
		}
		current, ok, err := s.workspace(ctx, tx, w.Username)
		if err != nil {
			return err
		}
		if current.Version != expected {
			return workspace.ErrChanged
		}
		w.Version = expected + 1
		args := []any{w.Base, items, rv, w.Version, toMicros(w.UpdatedAt), w.UpdatedBy, w.Username}
		if ok {
			_, err = s.exec(ctx, tx, "UPDATE smcp_workspaces SET base_id = ?, items = ?, review = ?, version = ?, "+
				"updated_us = ?, updated_by = ? WHERE username = ?", args...)
		} else {
			_, err = s.exec(ctx, tx, "INSERT INTO smcp_workspaces (base_id, items, review, version, updated_us, "+
				"updated_by, username) VALUES (?, ?, ?, ?, ?, ?, ?)", args...)
		}
		if err != nil {
			return err
		}
		return s.logWrite(ctx, tx, w, entry)
	})
	if err != nil {
		if errors.Is(err, workspace.ErrChanged) {
			return workspace.Workspace{}, err
		}
		return workspace.Workspace{}, fmt.Errorf("configstore: write workspace: %w", err)
	}
	return w, nil
}

func encodeWorkspace(w workspace.Workspace) (string, string, error) {
	var items []byte
	if w.Items != nil {
		var err error
		if items, err = json.Marshal(w.Items); err != nil {
			return "", "", err
		}
	}
	rv, err := json.Marshal(review{Conflicts: w.Conflicts, Notes: w.Notes, Taken: w.Taken, Checkpoint: w.Checkpoint,
		CheckpointAt: w.CheckpointAt})
	return string(items), string(rv), err
}

func (s *SQLStore) logWrite(ctx context.Context, tx *sql.Tx, w workspace.Workspace, e workspace.LogEntry) error {
	items, err := json.Marshal(e.Items)
	if err != nil {
		return err
	}
	_, err = s.exec(ctx, tx, "INSERT INTO smcp_workspace_log (username, version, at_us, author, op, items, note) "+
		"VALUES (?, ?, ?, ?, ?, ?, ?)", w.Username, w.Version, toMicros(w.UpdatedAt), e.By, e.Op, string(items), e.Note)
	return err
}

// WorkspaceLog implements workspace.Store.
func (s *SQLStore) WorkspaceLog(ctx context.Context, username string, limit int) ([]workspace.LogEntry, error) {
	q := "SELECT version, at_us, author, op, items, note FROM smcp_workspace_log WHERE username = ? ORDER BY version DESC"
	args := []any{username}
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, s.dialect.rebind(q), args...)
	if err != nil {
		return nil, fmt.Errorf("configstore: read workspace log: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []workspace.LogEntry
	for rows.Next() {
		var e workspace.LogEntry
		var at int64
		var items string
		if err := rows.Scan(&e.Version, &at, &e.By, &e.Op, &items, &e.Note); err != nil {
			return nil, err
		}
		e.At = fromMicros(at)
		if err := json.Unmarshal([]byte(items), &e.Items); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
