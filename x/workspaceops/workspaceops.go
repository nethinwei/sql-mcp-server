// Package workspaceops is the working-copy half of configuration versioning
// as a service: reading a workspace, writing items of it (several writers
// racing merge three ways), updating it onto a newer published revision,
// taking a draft in, resolving conflicts, saving a checkpoint (a draft to
// share) and committing (publishing it as a new revision), each write
// logged. The admin API and the configuration MCP endpoint both call it.
//
// It knows items and revisions, not configurations: a Codec turns revisions
// into items and items back into a payload, and a workspace.Schema holds the
// merge rules.
package workspaceops

import (
	"context"
	"errors"
	"slices"
	"strconv"

	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/core/workspace"
	"github.com/nethinwei/sql-mcp-server/x/revisionops"
)

// Codec turns configurations into workspace items and back.
type Codec interface {
	// Items is rev's configuration as items.
	Items(rev revision.Revision) (workspace.Snapshot, error)
	// Build is the normalized payload items make on base; an error when it
	// does not hold together (a workspace may not, while edited).
	Build(base revision.Revision, items workspace.Snapshot) ([]byte, error)
}

// History stores, publishes and discards revisions (revisionops.Service
// does, with its checks).
type History interface {
	Draft(ctx context.Context, req revisionops.DraftRequest) (revision.Revision, bool, error)
	Publish(ctx context.Context, req revisionops.PublishRequest) (revision.Revision, error)
	Discard(ctx context.Context, id int64, author string) (revision.Revision, error)
}

// Reconciler keeps a configuration's items consistent with each other as a
// write changes them (before to after): the references to an entity renamed
// or removed, for one. renames maps an item written to the item it was
// renamed or moved from. It returns after with what that changes.
type Reconciler func(before, after workspace.Snapshot, renames map[workspace.Ref]workspace.Ref) (
	workspace.Snapshot, error)

// Service performs workspace operations. It holds no per-request state.
type Service struct {
	Store  workspace.Store
	Codec  Codec
	Schema workspace.Schema
	// Revisions is the history; History writes it.
	Revisions revision.Store
	History   History
	// Reconcile, when set, runs on every write, in the same version.
	Reconcile Reconciler
	// Cache, when set, keeps the items of revisions read.
	Cache *ItemsCache
}

// Errors returned by the operations.
var (
	// ErrConflicts: open conflicts block saving and committing.
	ErrConflicts = errors.New("workspace: resolve the conflicts first")
	// ErrNothingPublished: a workspace builds on the published configuration.
	ErrNothingPublished = errors.New("workspace: nothing is published")
	// ErrBehind: the workspace builds on an older revision than the
	// published one; it commits only after an update (fast-forward only).
	ErrBehind = errors.New("workspace: it builds on an older revision than the published one: update it first")
	// ErrNoConflict: no open conflict has the ID given.
	ErrNoConflict = errors.New("workspace: no such open conflict")
)

// attempts bounds how often an operation reads the workspace again when
// another write came between its read and its write.
const attempts = 5

// State is a workspace as it stands: as kept (its items the base's when
// nothing was written), the base revision's items, and the open conflicts.
type State struct {
	Workspace workspace.Workspace
	Base      workspace.Snapshot
	Open      []workspace.Open
}

// Read is username's workspace; false when none is kept and nothing is
// published.
func (s Service) Read(ctx context.Context, username string) (State, bool, error) {
	w, base, ok, err := s.load(ctx, username)
	if err != nil || !ok {
		return State{}, ok, err
	}
	return s.state(w, base), true, nil
}

func (s Service) state(w workspace.Workspace, base workspace.Snapshot) State {
	return State{Workspace: w, Base: base, Open: s.Schema.OpenConflicts(w.Items, base, w.Conflicts)}
}

// load reads username's workspace, its items filled in, with its base's items.
func (s Service) load(ctx context.Context, username string) (workspace.Workspace, workspace.Snapshot, bool, error) {
	w, ok, err := s.Store.Workspace(ctx, username)
	if err != nil {
		return w, nil, false, err
	}
	if !ok {
		rev, err := s.Revisions.Published(ctx)
		if errors.Is(err, revision.ErrNoPublished) {
			return w, nil, false, nil
		}
		if err != nil {
			return w, nil, false, err
		}
		w = workspace.Workspace{Username: username, Base: rev.ID}
	}
	base, err := s.items(ctx, w.Base)
	if err != nil {
		return w, nil, false, err
	}
	if w.Items == nil {
		w.Items = base
	}
	return w, base, true, nil
}

// items is revision id's configuration as items.
func (s Service) items(ctx context.Context, id int64) (workspace.Snapshot, error) {
	if s.Cache != nil {
		if items, ok := s.Cache.get(id); ok {
			return items, nil
		}
	}
	rev, err := s.Revisions.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.itemsOf(rev)
}

// itemsOf is rev's configuration as items, kept in the cache.
func (s Service) itemsOf(rev revision.Revision) (workspace.Snapshot, error) {
	if s.Cache == nil {
		return s.Codec.Items(rev)
	}
	return s.Cache.Items(rev.ID, func() (workspace.Snapshot, error) { return s.Codec.Items(rev) })
}

// change is what an operation does to a workspace it read: it edits w (and
// may move it to another base, whose items it returns) and says what to log.
type change func(w *workspace.Workspace, base workspace.Snapshot) (workspace.Snapshot, workspace.LogEntry, error)

// apply runs fn on username's workspace and writes the result at the
// version read, reading again when another write came between.
func (s Service) apply(ctx context.Context, username, by string, fn change) (State, error) {
	for attempt := 0; ; attempt++ {
		w, base, ok, err := s.load(ctx, username)
		if err != nil {
			return State{}, err
		}
		if !ok {
			return State{}, ErrNothingPublished
		}
		read := w.Version
		newBase, entry, err := fn(&w, base)
		if err != nil {
			return State{}, err
		}
		if newBase != nil {
			base = newBase
		}
		w.UpdatedBy, entry.By = by, by
		saved, err := s.Store.PutWorkspace(ctx, w, read, entry)
		if err == nil {
			return s.state(saved, base), nil
		}
		if !errors.Is(err, workspace.ErrChanged) || attempt == attempts-1 {
			return State{}, err
		}
	}
}

// Write is an item written: its value (Missing removes it) and the value
// its writer read (Missing: absent). From, when set, is the item it was
// renamed or moved from, which it replaces (see Reconciler).
type Write struct {
	Ref             workspace.Ref
	Value, Previous any
	From            *workspace.Ref
}

// WriteItems writes items of username's workspace. An item written meanwhile
// (by another console, an agent) since its writer read it merges three ways
// (read, written, current): changes to different values both stay, and a
// value both changed differently keeps the written one, as a conflict
// against the current one. Nothing is lost. note, if any, says why.
func (s Service) WriteItems(ctx context.Context, username, by, op string, writes []Write, note *workspace.Note) (
	State, error,
) {
	writes = renamesLast(writes)
	return s.apply(ctx, username, by, func(w *workspace.Workspace, base workspace.Snapshot) (
		workspace.Snapshot, workspace.LogEntry, error,
	) {
		before := w.Items
		values, refs, renames, found := s.batch(w, writes)
		w.Items = before.Apply(values, refs)
		var err error
		if w.Items, refs, err = s.reconcile(before, w.Items, renames, refs); err != nil {
			return nil, workspace.LogEntry{}, err
		}
		w.Conflicts = s.Schema.Carry(w.Conflicts, found, w.Items, base, nil)
		entry := workspace.LogEntry{Op: op, Items: refs}
		if note != nil && note.Text != "" {
			entry.Note = note.Text
			if !slices.Contains(w.Notes, *note) {
				w.Notes = append(w.Notes, *note)
			}
		}
		return nil, entry, nil
	})
}

// batch applies writes at once over w's items: values holds what each ref
// becomes, read back by the writes that follow it in the same batch; a
// write whose previous value is not the current one merges with it, and
// found keeps what conflicted. Renames are as batchState.rename has them.
func (s Service) batch(w *workspace.Workspace, writes []Write) (
	map[workspace.Ref]any, []workspace.Ref, map[workspace.Ref]workspace.Ref, []workspace.Conflict,
) {
	var found []workspace.Conflict
	b := newBatchState(w.Items, writes)
	for _, wr := range writes {
		b.later[wr.Ref]--
		if wr.From != nil && *wr.From != wr.Ref {
			b.rename(wr.Ref, *wr.From)
		}
		value := wr.Value
		if was := b.current(wr.Ref); !s.Schema.Same(was, wr.Previous) {
			var conflicts []workspace.ValueConflict
			value, conflicts = s.Schema.Merge(wr.Previous, wr.Value, was, nil)
			found = append(found, s.pinned(wr.Ref, conflicts, workspace.Source{Kind: workspace.SourceWorkspace,
				By: w.UpdatedBy})...)
		}
		b.values[wr.Ref] = value
		b.refs = append(b.refs, wr.Ref)
	}
	return b.values, b.refs, b.renames, found
}

// batchState is a batch of writes being applied over before.
type batchState struct {
	before  workspace.Snapshot
	at      map[workspace.Ref]int
	values  map[workspace.Ref]any
	refs    []workspace.Ref
	renames map[workspace.Ref]workspace.Ref
	// written are the refs the batch writes; later counts the writes of
	// each still to come.
	written map[workspace.Ref]bool
	later   map[workspace.Ref]int
}

func newBatchState(before workspace.Snapshot, writes []Write) *batchState {
	b := &batchState{before: before, at: before.Index(), values: map[workspace.Ref]any{},
		refs: make([]workspace.Ref, 0, len(writes)), renames: map[workspace.Ref]workspace.Ref{},
		written: make(map[workspace.Ref]bool, len(writes)), later: make(map[workspace.Ref]int, len(writes))}
	for _, wr := range writes {
		b.written[wr.Ref] = true
		b.later[wr.Ref]++
	}
	return b
}

// current is what ref holds at this point of the batch.
func (b *batchState) current(ref workspace.Ref) any {
	if v, ok := b.values[ref]; ok {
		return v
	}
	if i, ok := b.at[ref]; ok {
		return b.before[i].Value
	}
	return workspace.Missing
}

// rename records the write of to as the rename of from, and vacates from:
//   - an item of the workspace moves, and its key goes missing, unless the
//     batch writes it too (a swap, or its removal);
//   - an item an earlier write of the batch made (a chain: a to b, b to c)
//     moves on: the rename is from where it came (c from a), and its key
//     goes missing unless a later write takes it;
//   - an item that is not there (a retry of a rename done) is a write
//     alone: what named the item already follows it.
func (b *batchState) rename(to, from workspace.Ref) {
	switch {
	case !workspace.IsMissing(b.before.Get(from)):
		b.renames[to] = from
		if b.written[from] || workspace.IsMissing(b.current(from)) {
			return
		}
	case !workspace.IsMissing(b.current(from)):
		if origin, ok := b.renames[from]; ok {
			b.renames[to] = origin
			delete(b.renames, from)
		}
		if b.later[from] > 0 {
			return
		}
	default:
		return
	}
	b.values[from] = workspace.Missing
	b.refs = append(b.refs, from)
}

// renamesLast puts the renames after the other writes: the removal of the
// item a rename replaces, written with the value its writer read, then
// merges first, so a change made to that item meanwhile is a conflict
// instead of going with it.
func renamesLast(writes []Write) []Write {
	rank := func(w Write) int {
		if w.From != nil && *w.From != w.Ref {
			return 1
		}
		return 0
	}
	out := slices.Clone(writes)
	slices.SortStableFunc(out, func(a, b Write) int { return rank(a) - rank(b) })
	return out
}

// reconcile runs Reconcile on a write (before to after), adding the items it
// changes to those written (refs).
func (s Service) reconcile(before, after workspace.Snapshot, renames map[workspace.Ref]workspace.Ref,
	refs []workspace.Ref,
) (workspace.Snapshot, []workspace.Ref, error) {
	if s.Reconcile == nil {
		return after, refs, nil
	}
	reconciled, err := s.Reconcile(before, after, renames)
	if err != nil {
		return nil, nil, err
	}
	for _, ref := range s.Schema.Changes(after, reconciled) {
		if !slices.Contains(refs, ref) {
			refs = append(refs, ref)
		}
	}
	return reconciled, refs, nil
}

// pinned are conflicts holding the other side's value.
func (s Service) pinned(ref workspace.Ref, vcs []workspace.ValueConflict, src workspace.Source) []workspace.Conflict {
	out := make([]workspace.Conflict, 0, len(vcs))
	for _, vc := range vcs {
		c := workspace.Conflict{ID: workspace.NewID(), Ref: ref, Path: vc.Path, Source: src, Pinned: true,
			Theirs: vc.Theirs, TheirsGone: workspace.IsMissing(vc.Theirs)}
		if c.TheirsGone {
			c.Theirs = nil
		}
		out = append(out, c)
	}
	return out
}

// Update moves username's workspace onto revision to (a newer published
// one), re-applying what was changed here: a value both changed differently
// keeps the workspace's, as a conflict against to.
func (s Service) Update(ctx context.Context, username, by string, to int64) (State, error) {
	next, err := s.items(ctx, to)
	if err != nil {
		return State{}, err
	}
	return s.apply(ctx, username, by, func(w *workspace.Workspace, base workspace.Snapshot) (
		workspace.Snapshot, workspace.LogEntry, error,
	) {
		changed := s.Schema.Changes(base, w.Items)
		s.moveOnto(w, base, w.Items, next, to)
		return next, workspace.LogEntry{Op: "update", Items: changed, Note: "#" + idOf(to)}, nil
	})
}

// moveOnto moves w onto revision to (items next), re-applying the changes
// from old to local.
func (s Service) moveOnto(w *workspace.Workspace, old, local, next workspace.Snapshot, to int64) {
	src := workspace.Source{Kind: workspace.SourcePublished, ID: idOf(to)}
	now, found := s.Schema.Replay(old, local, next, src, false)
	w.Conflicts = s.Schema.Carry(w.Conflicts, found, now, next, &src)
	w.Items, w.Base = now, to
}

// Reset makes username's workspace revision to as it is: what was changed
// here, the conflicts and the notes are dropped, and so is the record of the
// drafts taken in or saved from it (they are drafts still, offered again).
func (s Service) Reset(ctx context.Context, username, by string, to int64) (State, error) {
	next, err := s.items(ctx, to)
	if err != nil {
		return State{}, err
	}
	return s.apply(ctx, username, by, func(w *workspace.Workspace, base workspace.Snapshot) (
		workspace.Snapshot, workspace.LogEntry, error,
	) {
		changed := s.Schema.Changes(base, w.Items)
		w.Items, w.Base, w.Conflicts, w.Notes = next, to, nil, nil
		w.Taken, w.Checkpoint, w.CheckpointAt = nil, 0, 0
		return next, workspace.LogEntry{Op: "reset", Items: changed, Note: "#" + idOf(to)}, nil
	})
}

// Resolve resolves conflict id: theirs takes the other side's value, else
// the workspace's stays.
func (s Service) Resolve(ctx context.Context, username, by, id string, theirs bool) (State, error) {
	return s.apply(ctx, username, by, func(w *workspace.Workspace, base workspace.Snapshot) (
		workspace.Snapshot, workspace.LogEntry, error,
	) {
		var ref workspace.Ref
		for _, c := range w.Conflicts {
			if c.ID == id {
				ref = c.Ref
			}
		}
		items, left, ok := s.Schema.Resolve(w.Items, base, w.Conflicts, id, theirs)
		if !ok {
			return nil, workspace.LogEntry{}, ErrNoConflict
		}
		w.Items, w.Conflicts = items, left
		return nil, workspace.LogEntry{Op: "resolve", Items: []workspace.Ref{ref}}, nil
	})
}

// Log is username's workspace log, newest first.
func (s Service) Log(ctx context.Context, username string, limit int) ([]workspace.LogEntry, error) {
	return s.Store.WorkspaceLog(ctx, username, limit)
}

func idOf(id int64) string { return strconv.FormatInt(id, 10) }
