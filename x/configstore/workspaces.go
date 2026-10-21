package configstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

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

// keptWorkspace is a workspace as last read or written, at its version and
// write time (a workspace is never removed, so the two name its content).
type keptWorkspace struct {
	version, updated int64
	w                workspace.Workspace
}

// Workspace implements workspace.Store. A large workspace takes long to
// decode and every operation reads it: the one last read or written is
// kept, and returned while the stored one is still at its version.
func (s *SQLStore) Workspace(ctx context.Context, username string) (workspace.Workspace, bool, error) {
	if v, ok := s.workspaces.Load(username); ok {
		k := v.(keptWorkspace)
		var version, updated int64
		err := s.queryRow(ctx, s.db, "SELECT version, updated_us FROM smcp_workspaces WHERE username = ?", username).
			Scan(&version, &updated)
		if err == nil && version == k.version && updated == k.updated {
			return k.copy(), true, nil
		}
	}
	w, ok, err := s.workspace(ctx, s.db, username)
	if ok && err == nil {
		s.keep(w)
	}
	return w, ok, err
}

// keep keeps a copy of w: the caller goes on editing its own.
func (s *SQLStore) keep(w workspace.Workspace) {
	s.workspaces.Store(w.Username, keptWorkspace{version: w.Version, updated: toMicros(w.UpdatedAt), w: cloneLists(w)})
}

// copy is the kept workspace for a caller to edit.
func (k keptWorkspace) copy() workspace.Workspace { return cloneLists(k.w) }

// cloneLists copies w's lists, which callers edit in place (removing a note
// shifts the others), so that no two workspaces share one; the values in
// them are shared, as they are never written to.
func cloneLists(w workspace.Workspace) workspace.Workspace {
	w.Items, w.Conflicts, w.Notes, w.Taken = slices.Clone(w.Items), slices.Clone(w.Conflicts), slices.Clone(w.Notes),
		slices.Clone(w.Taken)
	return w
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

// PutWorkspace implements workspace.Store, writing over the version expected.
func (s *SQLStore) PutWorkspace(
	ctx context.Context, w workspace.Workspace, expected int64, entry workspace.LogEntry,
) (workspace.Workspace, error) {
	items, rv, err := encodeWorkspace(w)
	if err != nil {
		return workspace.Workspace{}, err
	}
	w.UpdatedAt = s.timestamp()
	if expected > 0 {
		err = s.updateWorkspace(ctx, w, items, rv, expected, entry)
	} else {
		err = s.insertWorkspace(ctx, w, items, rv, entry)
	}
	if err != nil {
		if errors.Is(err, workspace.ErrChanged) {
			return workspace.Workspace{}, err
		}
		return workspace.Workspace{}, fmt.Errorf("configstore: write workspace: %w", err)
	}
	w.Version = expected + 1
	s.keep(w)
	return w, nil
}

// updateWorkspace writes w over version expected of the workspace, by that
// version alone: workspaces are written one by one, and not under the store
// lock publishes take (a console saves every few hundred milliseconds).
func (s *SQLStore) updateWorkspace(
	ctx context.Context, w workspace.Workspace, items, rv string, expected int64, entry workspace.LogEntry,
) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		w.Version = expected + 1
		r, err := s.exec(ctx, tx, "UPDATE smcp_workspaces SET base_id = ?, items = ?, review = ?, version = ?, "+
			"updated_us = ?, updated_by = ? WHERE username = ? AND version = ?",
			w.Base, items, rv, w.Version, toMicros(w.UpdatedAt), w.UpdatedBy, w.Username, expected)
		if err != nil {
			return err
		}
		if n, err := r.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return workspace.ErrChanged
		}
		return s.logWrite(ctx, tx, w, entry)
	})
}

// insertWorkspace writes a workspace's first version, under the store lock:
// two first writes find each other.
func (s *SQLStore) insertWorkspace(
	ctx context.Context, w workspace.Workspace, items, rv string, entry workspace.LogEntry,
) error {
	expected := int64(0)
	return s.locked(ctx, func(tx *sql.Tx) error {
		// Only the version is compared: the items are not decoded again.
		var current int64
		err := s.queryRow(ctx, tx, "SELECT version FROM smcp_workspaces WHERE username = ?", w.Username).Scan(&current)
		ok := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if current != expected {
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
	out, err := queryAll(ctx, s, s.db, scanLogEntry, q, args...)
	if err != nil {
		return nil, fmt.Errorf("configstore: read workspace log: %w", err)
	}
	return out, nil
}

func scanLogEntry(row scanner) (workspace.LogEntry, error) {
	var e workspace.LogEntry
	var at int64
	var items string
	if err := row.Scan(&e.Version, &at, &e.By, &e.Op, &items, &e.Note); err != nil {
		return workspace.LogEntry{}, err
	}
	e.At = fromMicros(at)
	return e, json.Unmarshal([]byte(items), &e.Items)
}
