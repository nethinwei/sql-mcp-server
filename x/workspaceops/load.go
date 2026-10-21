package workspaceops

import (
	"context"
	"errors"

	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/core/workspace"
)

// Load makes the content of revision to (any revision: an older one, a
// discarded draft) username's workspace, as changes to the published
// revision, which it then builds on: published as it is, they restore to.
// Without keep, what was changed here, the conflicts, the notes and the
// record of the drafts taken in or saved from it are dropped, as by Reset.
// With keep, what was changed here is re-applied onto to: a value both
// changed differently keeps the workspace's, as a conflict holding to's.
func (s Service) Load(ctx context.Context, username, by string, to int64, keep bool) (State, error) {
	rev, err := s.Revisions.Get(ctx, to)
	if err != nil {
		return State{}, err
	}
	content, err := s.itemsOf(rev)
	if err != nil {
		return State{}, err
	}
	for attempt := 0; ; attempt++ {
		// The published revision is read again when the workspace moved past
		// it meanwhile: loading onto it would move the workspace back.
		st, err := s.loadRevision(ctx, username, by, rev, content, keep)
		if !errors.Is(err, errPublishedMoved) || attempt == attempts-1 {
			return st, err
		}
	}
}

func (s Service) loadRevision(
	ctx context.Context, username, by string, rev revision.Revision, content workspace.Snapshot, keep bool,
) (State, error) {
	published, err := s.Revisions.PublishedSummary(ctx)
	if errors.Is(err, revision.ErrNoPublished) {
		return State{}, ErrNothingPublished
	}
	if err != nil {
		return State{}, err
	}
	onto, err := s.publishedItems(ctx, published.ID)
	if err != nil {
		return State{}, err
	}
	return s.apply(ctx, username, by, func(w *workspace.Workspace, base workspace.Snapshot) (
		workspace.Snapshot, workspace.LogEntry, error,
	) {
		if w.Base > published.ID {
			return nil, workspace.LogEntry{}, errPublishedMoved
		}
		before := w.Items
		if keep {
			src := workspace.Source{Kind: workspace.SourcePublished, ID: idOf(rev.ID)}
			if rev.State.Unpublished() {
				src.Kind = workspace.SourceDraft
			}
			now, found := s.Schema.Replay(base, w.Items, content, src, true)
			// The conflicts against the published revision are now against
			// the one built on.
			at := workspace.Source{Kind: workspace.SourcePublished, ID: idOf(published.ID)}
			w.Conflicts = s.Schema.Carry(w.Conflicts, found, now, onto, &at)
			w.Items = now
		} else {
			w.Items, w.Conflicts, w.Notes = content, nil, nil
			w.Taken, w.Checkpoint, w.CheckpointAt = nil, 0, 0
		}
		w.Base = published.ID
		return onto, workspace.LogEntry{Op: "load", Items: s.Schema.Changes(before, w.Items), Note: "#" + idOf(rev.ID)},
			nil
	})
}
