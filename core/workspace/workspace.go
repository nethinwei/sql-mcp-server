// Package workspace is the working-copy half of configuration versioning
// (the history half is core/revision): each administrator has one workspace,
// edited by them (in any console) and by their agents, and committed as a
// revision. A workspace holds items (what a codec makes of a configuration:
// an entity, a role, the settings...) that may be invalid while edited, like
// code half written. Values merge three ways (see Merge): changes made on
// both sides to the same value are conflicts, which keep the local value and
// block committing until resolved; nothing is overwritten silently.
//
// The package knows items, not what they mean: the rules a merge needs (how
// list elements are keyed, which text is optional) come in a Schema.
package workspace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"time"
)

// Ref names an item: its kind and its key within the kind.
type Ref struct {
	Kind string `json:"kind"`
	Key  string `json:"key"`
}

// Item is one item with its value (decoded JSON).
type Item struct {
	Ref
	Value any `json:"value"`
}

// Snapshot is a configuration as items, in order. Lookups by ref walk it;
// an operation that looks many up indexes it first (Index), and one that
// changes many items applies them at once (Apply): a configuration may hold
// thousands of items.
type Snapshot []Item

// Get is the value of ref, or Missing.
func (s Snapshot) Get(ref Ref) any {
	if i := s.index(ref); i >= 0 {
		return s[i].Value
	}
	return Missing
}

func (s Snapshot) index(ref Ref) int {
	return slices.IndexFunc(s, func(it Item) bool { return it.Ref == ref })
}

// Index is the position of each item by ref.
func (s Snapshot) Index() map[Ref]int {
	out := make(map[Ref]int, len(s))
	for i, it := range s {
		out[it.Ref] = i
	}
	return out
}

// With returns s with ref's value replaced where it is, appended when
// absent, or removed when value is Missing.
func (s Snapshot) With(ref Ref, value any) Snapshot {
	return s.Apply(map[Ref]any{ref: value}, []Ref{ref})
}

// Apply returns s with each ref of order set to its value in values:
// replaced where it is, appended when absent (in that order), or removed
// when the value is Missing. It walks s once.
func (s Snapshot) Apply(values map[Ref]any, order []Ref) Snapshot {
	out := make(Snapshot, 0, len(s)+len(order))
	for _, it := range s {
		if v, ok := values[it.Ref]; ok {
			if !IsMissing(v) {
				out = append(out, Item{Ref: it.Ref, Value: v})
			}
			continue
		}
		out = append(out, it)
	}
	present := s.Index()
	for _, ref := range order {
		if _, ok := present[ref]; ok {
			continue
		}
		if v := values[ref]; v != nil && !IsMissing(v) {
			out = append(out, Item{Ref: ref, Value: v})
			present[ref] = len(out) - 1
		}
	}
	return out
}

// Changes are the refs whose value differs between a and b.
func (sc Schema) Changes(a, b Snapshot) []Ref {
	var out []Ref
	at := a.Index()
	for _, it := range b {
		was := Missing
		if i, ok := at[it.Ref]; ok {
			was = a[i].Value
		}
		if !sc.Same(was, it.Value) {
			out = append(out, it.Ref)
		}
	}
	in := b.Index()
	for _, it := range a {
		if _, ok := in[it.Ref]; !ok {
			out = append(out, it.Ref)
		}
	}
	return out
}

// Source is where the other side of a conflict comes from: a revision
// published since the workspace's base (published), a draft taken in
// (draft), or a write of the same workspace made meanwhile (workspace: by
// another console or an agent).
type Source struct {
	Kind string `json:"kind"`
	// ID is the revision or draft; By who wrote, for a workspace write.
	ID string `json:"id,omitempty"`
	By string `json:"by,omitempty"`
}

// The kinds of sources.
const (
	SourcePublished = "published"
	SourceDraft     = "draft"
	SourceWorkspace = "workspace"
)

// Conflict is a value of an item two sides changed differently. The
// workspace keeps its own value; the other side's is the base's (a
// published revision's) or, when pinned, the one held here.
type Conflict struct {
	ID     string `json:"id"`
	Ref    Ref    `json:"ref"`
	Path   Path   `json:"path"`
	Source Source `json:"source"`
	Pinned bool   `json:"pinned,omitempty"`
	// Theirs (pinned only), TheirsGone when that side removed it.
	Theirs     any  `json:"theirs,omitempty"`
	TheirsGone bool `json:"theirsGone,omitempty"`
}

// Open is a conflict as it stands: the workspace's value and the other side's.
type Open struct {
	Conflict
	Mine, TheirsValue any
}

// Note says where changes came from: a plan an agent applied or a draft
// taken in, with what its author said.
type Note struct {
	Source string `json:"source"`
	By     string `json:"by"`
	Text   string `json:"text"`
}

// Workspace is an administrator's workspace as kept.
type Workspace struct {
	Username string
	// Base is the revision it builds on; Items its content, nil the base's.
	Base      int64
	Items     Snapshot
	Conflicts []Conflict
	Notes     []Note
	// Taken are drafts taken in; Checkpoint the draft last saved from it,
	// at version CheckpointAt (the workspace holds it while still there).
	Taken        []int64
	Checkpoint   int64
	CheckpointAt int64
	Version      int64
	UpdatedAt    time.Time
	// UpdatedBy is who wrote last: the administrator or agent:<token name>.
	UpdatedBy string
}

// LogEntry records a write of a workspace.
type LogEntry struct {
	Version int64     `json:"version"`
	At      time.Time `json:"at"`
	By      string    `json:"by"`
	// Op is what wrote it (edit, apply, update, merge, resolve, reset,
	// commit, checkpoint); Items what changed.
	Op    string `json:"op"`
	Items []Ref  `json:"items"`
	Note  string `json:"note,omitempty"`
}

// ErrChanged: the workspace was written since it was read.
var ErrChanged = errors.New("workspace: changed since it was read")

// Store keeps workspaces and their write logs.
type Store interface {
	// Workspace is username's workspace; false when none is kept.
	Workspace(ctx context.Context, username string) (Workspace, bool, error)
	// PutWorkspace writes w while the kept one is at version expected (0:
	// none), recording entry with the new version, in one transaction; it
	// returns w as written, or ErrChanged.
	PutWorkspace(ctx context.Context, w Workspace, expected int64, entry LogEntry) (Workspace, error)
	// WorkspaceLog is username's log, newest first; limit <= 0 is all.
	WorkspaceLog(ctx context.Context, username string, limit int) ([]LogEntry, error)
}

// NewID is a random conflict ID.
func NewID() string {
	b := make([]byte, 9)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
