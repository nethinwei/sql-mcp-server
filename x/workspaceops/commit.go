package workspaceops

import (
	"context"
	"fmt"
	"slices"

	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/core/workspace"
	"github.com/nethinwei/sql-mcp-server/x/revisionops"
)

// MergeDraft takes draft (another administrator's checkpoint) into
// username's workspace, onto the published revision: what the draft changed
// since the revision it builds on joins the workspace, and what was changed
// here stays. A value the draft and a newer publish changed differently
// keeps the draft's, as a conflict against the published one; a value the
// draft and this workspace changed differently keeps the workspace's, as a
// conflict against the draft's. Taking a draft twice does nothing.
func (s Service) MergeDraft(ctx context.Context, username, by string, draft int64) (State, error) {
	d, err := s.Revisions.Get(ctx, draft)
	if err != nil {
		return State{}, err
	}
	from, err := revision.BasedOn(ctx, s.Revisions, d)
	if err != nil {
		return State{}, err
	}
	published, err := s.Revisions.Published(ctx)
	if err != nil {
		return State{}, err
	}
	snaps := map[int64]workspace.Snapshot{}
	for _, rev := range []revision.Revision{d, published} {
		if snaps[rev.ID], err = s.itemsOf(rev); err != nil {
			return State{}, err
		}
	}
	if snaps[from], err = s.items(ctx, from); err != nil {
		return State{}, err
	}
	return s.apply(ctx, username, by, func(w *workspace.Workspace, base workspace.Snapshot) (
		workspace.Snapshot, workspace.LogEntry, error,
	) {
		if slices.Contains(w.Taken, draft) {
			return nil, workspace.LogEntry{Op: "merge", Note: "#" + idOf(draft)}, nil
		}
		onto, before := snaps[published.ID], w.Items
		if w.Base != published.ID {
			s.moveOnto(w, base, w.Items, onto, published.ID)
		}
		prior, local := w.Conflicts, w.Items
		pub := workspace.Source{Kind: workspace.SourcePublished, ID: idOf(published.ID)}
		withDraft, againstPublished := s.Schema.Replay(snaps[from], snaps[draft], onto, pub, false)
		now, againstDraft := s.Schema.Replay(onto, local, withDraft,
			workspace.Source{Kind: workspace.SourceDraft, ID: idOf(draft)}, true)
		w.Items = now
		w.Conflicts = s.Schema.Carry(prior, append(againstPublished, againstDraft...), now, onto, nil)
		w.Taken = append(w.Taken, draft)
		if d.Comment != "" {
			w.Notes = append(w.Notes, workspace.Note{Source: "draft", By: idOf(draft), Text: d.Comment})
		}
		// What moving onto the published revision changed is the merge's too.
		entry := workspace.LogEntry{Op: "merge", Items: s.Schema.Changes(before, now), Note: "#" + idOf(draft)}
		return onto, entry, nil
	})
}

// Checkpoint saves username's workspace as a draft (to share with other
// administrators), in place of the one it saved before; it is taken
// already, so it is not offered back to this workspace. The draft holds the
// workspace as read: when it was written while saving, the workspace keeps
// the draft but no longer matches it (CheckpointAt).
func (s Service) Checkpoint(ctx context.Context, username, by, comment string) (revision.Revision, error) {
	st, ok, err := s.Read(ctx, username)
	if err != nil || !ok {
		return revision.Revision{}, orNothingPublished(err, ok)
	}
	if len(st.Open) > 0 {
		return revision.Revision{}, ErrConflicts
	}
	built := st.Workspace
	draft, err := s.build(ctx, built, by, comment)
	if err != nil {
		return revision.Revision{}, err
	}
	var earlier int64
	_, err = s.apply(ctx, username, by, func(w *workspace.Workspace, _ workspace.Snapshot) (
		workspace.Snapshot, workspace.LogEntry, error,
	) {
		earlier = w.Checkpoint
		// Unwritten, a workspace is the published revision: its version stays
		// while its base moves, so both must be as built.
		at := int64(0)
		if w.Version == built.Version && w.Base == built.Base {
			at = w.Version + 1
		}
		w.Checkpoint, w.CheckpointAt, w.Taken = draft.ID, at, append(w.Taken, draft.ID)
		return nil, workspace.LogEntry{Op: "checkpoint", Note: "#" + idOf(draft.ID)}, nil
	})
	if err != nil {
		return revision.Revision{}, err
	}
	if earlier != 0 {
		s.discard(ctx, earlier, by)
	}
	return draft, nil
}

// build stores w's content as a draft on its base.
func (s Service) build(ctx context.Context, w workspace.Workspace, by, comment string) (revision.Revision, error) {
	base, err := s.Revisions.Get(ctx, w.Base)
	if err != nil {
		return revision.Revision{}, err
	}
	payload, err := s.Codec.Build(base, w.Items)
	if err != nil {
		return revision.Revision{}, err
	}
	draft, _, err := s.History.Draft(ctx, revisionops.DraftRequest{Payload: payload, Parent: w.Base,
		Author: by, Comment: comment})
	return draft, err
}

// discard discards draft id when it is one still.
func (s Service) discard(ctx context.Context, id int64, by string) {
	if rev, err := s.Revisions.Get(ctx, id); err == nil && rev.State == revision.StateDraft {
		_, _ = s.History.Discard(ctx, id, by)
	}
}

// NotMovedError is a commit published whose workspace could not then be
// moved onto the revision: the revision serves, and the workspace still
// builds on the one before, with its notes and drafts, until it is updated.
type NotMovedError struct {
	Published revision.Revision
	Err       error
}

func (e *NotMovedError) Error() string {
	return fmt.Sprintf("workspace: published as #%d, but the workspace was not moved onto it (update it): %v",
		e.Published.ID, e.Err)
}

func (e *NotMovedError) Unwrap() error { return e.Err }

// CommitRequest commits a workspace as it was read.
type CommitRequest struct {
	// Version is the workspace version reviewed; another one is
	// workspace.ErrChanged (check it again).
	Version int64
	// ExpectedPublished is the revision the reviewer saw published.
	ExpectedPublished int64
	RestartRequired   bool
	Comment           string
}

// Commit publishes username's workspace, at the version reviewed, as a new
// revision: its content is built and checked, stored, and published only
// while ExpectedPublished is still the published one (else a
// *revisionops.ConflictError; the revision stored is discarded). The
// workspace then builds on the new revision, keeping what was written to
// it while committing; its notes are dropped and the drafts it saved or
// took in, published with it, are discarded. A workspace that cannot be
// moved onto it is a *NotMovedError, with the revision published.
func (s Service) Commit(ctx context.Context, username, by string, req CommitRequest) (revision.Revision, error) {
	st, ok, err := s.Read(ctx, username)
	if err != nil || !ok {
		return revision.Revision{}, orNothingPublished(err, ok)
	}
	snap := st.Workspace
	if snap.Version != req.Version {
		return revision.Revision{}, workspace.ErrChanged
	}
	if len(st.Open) > 0 {
		return revision.Revision{}, ErrConflicts
	}
	if snap.Base != req.ExpectedPublished {
		return revision.Revision{}, ErrBehind
	}
	// Published since: refused before a draft is stored (Publish checks again).
	if now, err := s.Revisions.Published(ctx); err != nil {
		return revision.Revision{}, err
	} else if now.ID != req.ExpectedPublished {
		return revision.Revision{}, &revisionops.ConflictError{Expected: req.ExpectedPublished, Published: now.ID}
	}
	draft, err := s.build(ctx, snap, by, req.Comment)
	if err != nil {
		return revision.Revision{}, err
	}
	expected := req.ExpectedPublished
	published, err := s.History.Publish(ctx, revisionops.PublishRequest{ID: draft.ID,
		RestartRequired: req.RestartRequired, ExpectedPublished: &expected, Author: by})
	if err != nil {
		s.discard(ctx, draft.ID, by)
		return revision.Revision{}, err
	}
	committed := s.Schema.Changes(st.Base, snap.Items)
	if err := s.moveAfterCommit(ctx, username, by, snap, published, committed); err != nil {
		return published, &NotMovedError{Published: published, Err: err}
	}
	for _, id := range append(slices.Clone(snap.Taken), snap.Checkpoint) {
		if id != 0 && id != published.ID {
			s.discard(ctx, id, by)
		}
	}
	return published, nil
}

// moveAfterCommit moves the workspace onto the revision snap was published
// as, re-applying what was written since snap. Only what snap held is done
// with (its notes, drafts taken and checkpoint, all published now): a draft
// taken in or saved while committing is the workspace's still.
func (s Service) moveAfterCommit(ctx context.Context, username, by string, snap workspace.Workspace,
	published revision.Revision, committed []workspace.Ref,
) error {
	next, err := s.itemsOf(published)
	if err != nil {
		return err
	}
	_, err = s.apply(ctx, username, by, func(w *workspace.Workspace, _ workspace.Snapshot) (
		workspace.Snapshot, workspace.LogEntry, error,
	) {
		s.moveOnto(w, snap.Items, w.Items, next, published.ID)
		w.Notes = slices.DeleteFunc(w.Notes, func(n workspace.Note) bool { return slices.Contains(snap.Notes, n) })
		w.Taken = slices.DeleteFunc(w.Taken, func(id int64) bool { return slices.Contains(snap.Taken, id) })
		if w.Checkpoint == snap.Checkpoint {
			w.Checkpoint = 0
		}
		w.CheckpointAt = 0
		return next, workspace.LogEntry{Op: "commit", Items: committed, Note: "#" + idOf(published.ID)}, nil
	})
	return err
}

func orNothingPublished(err error, ok bool) error {
	if err == nil && !ok {
		return ErrNothingPublished
	}
	return err
}
