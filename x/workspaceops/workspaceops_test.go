package workspaceops

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/core/workspace"
	"github.com/nethinwei/sql-mcp-server/x/revisionops"
)

var schema = workspace.Schema{
	Lists: map[string]string{"fields[]": "name", "grants[]": "entity"}, Unset: map[string]bool{"description": true},
}

// codec keeps a revision's items as its JSON payload; an item with
// "invalid": true does not build. onBuild runs while building.
type codec struct{ onBuild func() }

func (codec) Items(rev revision.Revision) (workspace.Snapshot, error) {
	var s workspace.Snapshot
	err := json.Unmarshal(rev.Payload, &s)
	return s, err
}

func (c codec) Build(_ revision.Revision, items workspace.Snapshot) ([]byte, error) {
	for _, it := range items {
		if m, ok := it.Value.(map[string]any); ok && m["invalid"] == true {
			return nil, errors.New("invalid")
		}
	}
	if c.onBuild != nil {
		c.onBuild()
	}
	return json.Marshal(items)
}

// history publishes on a revision store, fast-forward only, as revisionops does.
type history struct{ store revision.Store }

func (h history) Draft(ctx context.Context, req revisionops.DraftRequest) (revision.Revision, bool, error) {
	rev, err := h.store.Create(ctx, revision.Draft{ParentID: req.Parent, Payload: req.Payload, Author: req.Author,
		Comment: req.Comment})
	return rev, false, err
}

func (h history) Publish(ctx context.Context, req revisionops.PublishRequest) (revision.Revision, error) {
	return h.store.Publish(ctx, req.ID, *req.ExpectedPublished, revision.Meta{Author: req.Author})
}

func (h history) Discard(ctx context.Context, id int64, _ string) (revision.Revision, error) {
	return h.store.Discard(ctx, id)
}

type env struct {
	t     *testing.T
	ctx   context.Context
	revs  *revision.MemoryStore
	codec *codec
	svc   Service
}

func newEnv(t *testing.T) *env {
	revs := revision.NewMemoryStore(nil)
	c := &codec{}
	e := &env{t: t, ctx: context.Background(), revs: revs, codec: c}
	e.svc = Service{Store: workspace.NewMemoryStore(nil), Codec: c, Schema: schema, Revisions: revs,
		History: history{revs}}
	e.publish(`[{"kind":"role","key":"analyst","value":{"name":"analyst","description":"base","grants":[]}}]`)
	return e
}

// publish publishes items (JSON) as the next revision.
func (e *env) publish(items string) int64 {
	e.t.Helper()
	current, _ := e.revs.Published(e.ctx)
	rev, err := e.revs.Create(e.ctx, revision.Draft{ParentID: current.ID, Payload: []byte(items), Author: "other"})
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.revs.Publish(e.ctx, rev.ID, current.ID, revision.Meta{}); err != nil {
		e.t.Fatal(err)
	}
	return rev.ID
}

var analyst = workspace.Ref{Kind: "role", Key: "analyst"}

func role(description string, grants ...string) any {
	gs := []any{}
	for _, g := range grants {
		gs = append(gs, map[string]any{"entity": g, "actions": []any{"READ"}})
	}
	return map[string]any{"name": "analyst", "description": description, "grants": gs}
}

// items is the payload of a revision holding the analyst role only.
func items(analystRole any) string {
	data, _ := json.Marshal(workspace.Snapshot{{Ref: analyst, Value: analystRole}})
	return string(data)
}

func (e *env) read() State {
	e.t.Helper()
	st, ok, err := e.svc.Read(e.ctx, "root")
	if err != nil || !ok {
		e.t.Fatalf("read: %v %v", ok, err)
	}
	return st
}

func (e *env) write(by string, value, previous any) State {
	e.t.Helper()
	st, err := e.svc.WriteItems(e.ctx, "root", by, "edit", []Write{{Ref: analyst, Value: value, Previous: previous}}, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	return st
}

func (e *env) analyst() map[string]any { return e.read().Workspace.Items.Get(analyst).(map[string]any) }

func TestReadsThePublishedRevisionUntilWritten(t *testing.T) {
	e := newEnv(t)
	st := e.read()
	if st.Workspace.Version != 0 || st.Workspace.Base != 1 || !schema.Same(st.Workspace.Items.Get(analyst), role("base")) {
		t.Fatalf("state = %+v", st)
	}
}

// The administrator and an agent write the same item: different values
// both stay; the same value differently is a conflict keeping the later
// writer's, against the other's.
func TestConcurrentWritesMerge(t *testing.T) {
	e := newEnv(t)
	read := e.read().Workspace.Items.Get(analyst)
	e.write("agent:laptop", role("base", "orders"), read)
	st := e.write("root", role("mine"), read)
	if !schema.Same(st.Workspace.Items.Get(analyst), role("mine", "orders")) || len(st.Open) != 0 {
		t.Fatalf("merged = %+v", st)
	}
	read = st.Workspace.Items.Get(analyst)
	e.write("agent:laptop", role("agent", "orders"), read)
	st = e.write("root", role("again", "orders"), read)
	if len(st.Open) != 1 || st.Open[0].Source != (workspace.Source{Kind: workspace.SourceWorkspace, By: "agent:laptop"}) ||
		st.Open[0].Mine != "again" || st.Open[0].TheirsValue != "agent" {
		t.Fatalf("conflict = %+v", st.Open)
	}
	if _, err := e.svc.Commit(e.ctx, "root", "root", CommitRequest{Version: st.Workspace.Version,
		ExpectedPublished: 1}); !errors.Is(err, ErrConflicts) {
		t.Fatalf("committed with a conflict: %v", err)
	}
	st, err := e.svc.Resolve(e.ctx, "root", "root", st.Open[0].ID, true)
	if err != nil || len(st.Open) != 0 || e.analyst()["description"] != "agent" {
		t.Fatalf("resolved = %+v %v", st, err)
	}
}

// An update re-applies what was changed here onto the newer revision.
func TestUpdateOntoANewerRevision(t *testing.T) {
	e := newEnv(t)
	e.write("root", role("mine"), role("base"))
	next := e.publish(items(role("theirs", "refunds")))
	st, err := e.svc.Update(e.ctx, "root", "root", next)
	if err != nil || st.Workspace.Base != next || len(st.Open) != 1 ||
		st.Open[0].Source.Kind != workspace.SourcePublished ||
		!schema.Same(e.analyst()["grants"], role("", "refunds").(map[string]any)["grants"]) {
		t.Fatalf("updated = %+v %v", st, err)
	}
}

// A draft taken in joins the workspace once; a value it and the workspace
// changed differently is a conflict against the draft.
func TestMergeDraft(t *testing.T) {
	e := newEnv(t)
	draft, err := e.revs.Create(e.ctx, revision.Draft{ParentID: 1, Comment: "同事",
		Payload: []byte(items(role("draft", "x")))})
	if err != nil {
		t.Fatal(err)
	}
	e.write("root", role("mine"), role("base"))
	st, err := e.svc.MergeDraft(e.ctx, "root", "root", draft.ID)
	if err != nil || len(st.Open) != 1 || st.Open[0].Source.Kind != workspace.SourceDraft || !st.Open[0].Pinned ||
		len(st.Workspace.Notes) != 1 || len(e.analyst()["grants"].([]any)) != 1 {
		t.Fatalf("merged = %+v %v", st, err)
	}
	again, err := e.svc.MergeDraft(e.ctx, "root", "root", draft.ID)
	if err != nil || again.Workspace.Version != st.Workspace.Version+1 || len(again.Open) != 1 {
		t.Fatalf("merged twice = %+v %v", again, err)
	}
}

// A draft building on a discarded draft still brings in what that one
// changed: the discarded draft was never published, so it is not a base.
func TestMergeDraftOnADiscardedDraft(t *testing.T) {
	e := newEnv(t)
	parent, err := e.revs.Create(e.ctx, revision.Draft{ParentID: 1, Payload: []byte(items(role("parent")))})
	if err != nil {
		t.Fatal(err)
	}
	child, err := e.revs.Create(e.ctx, revision.Draft{ParentID: parent.ID,
		Payload: []byte(items(role("parent", "orders")))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.revs.Discard(e.ctx, parent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.MergeDraft(e.ctx, "root", "root", child.ID); err != nil {
		t.Fatal(err)
	}
	if a := e.analyst(); a["description"] != "parent" || len(a["grants"].([]any)) != 1 {
		t.Fatalf("merged = %v", a)
	}
}

// A reset drops what drafts brought in along with the record of taking them:
// the draft is offered again and merges again.
func TestResetForgetsTakenDrafts(t *testing.T) {
	e := newEnv(t)
	draft, err := e.revs.Create(e.ctx, revision.Draft{ParentID: 1, Payload: []byte(items(role("draft")))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.MergeDraft(e.ctx, "root", "root", draft.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Checkpoint(e.ctx, "root", "root", ""); err != nil {
		t.Fatal(err)
	}
	st, err := e.svc.Reset(e.ctx, "root", "root", 1)
	if w := st.Workspace; err != nil || len(w.Taken) != 0 || w.Checkpoint != 0 || w.CheckpointAt != 0 {
		t.Fatalf("reset = %+v %v", w, err)
	}
	if _, err := e.svc.MergeDraft(e.ctx, "root", "root", draft.ID); err != nil || e.analyst()["description"] != "draft" {
		t.Fatalf("merged again = %v, %v", e.analyst(), err)
	}
}

// A checkpoint holds the content it was built from: written meanwhile, the
// workspace no longer matches it, and saving again is offered.
func TestCheckpointWhileWriting(t *testing.T) {
	e := newEnv(t)
	e.write("root", role("mine"), role("base"))
	e.codec.onBuild = func() { // an agent writes while saving
		e.codec.onBuild = nil
		e.write("agent:laptop", role("agent"), role("mine"))
	}
	cp, err := e.svc.Checkpoint(e.ctx, "root", "root", "share")
	if err != nil {
		t.Fatal(err)
	}
	w := e.read().Workspace
	if w.Checkpoint != cp.ID || !slices.Contains(w.Taken, cp.ID) {
		t.Fatalf("the workspace does not hold the draft it saved: %+v", w)
	}
	if w.CheckpointAt == w.Version {
		t.Fatalf("draft #%d of the earlier content counts as the current content: %+v", cp.ID, w)
	}
}

// A commit publishes the version reviewed and moves the workspace onto it,
// keeping what was written meanwhile and discarding the drafts it held.
func TestCommit(t *testing.T) {
	e := newEnv(t)
	st := e.write("root", role("mine"), role("base"))
	cp, err := e.svc.Checkpoint(e.ctx, "root", "root", "share")
	if err != nil {
		t.Fatal(err)
	}
	if w := e.read().Workspace; w.Checkpoint != cp.ID || w.CheckpointAt != w.Version {
		t.Fatalf("the workspace does not hold its checkpoint: %+v", w)
	}
	if _, err := e.svc.Commit(e.ctx, "root", "root", CommitRequest{Version: st.Workspace.Version,
		ExpectedPublished: 1}); !errors.Is(err, workspace.ErrChanged) {
		t.Fatalf("a commit of another version: %v", err)
	}
	st = e.read()
	other := workspace.Ref{Kind: "role", Key: "support"}
	e.codec.onBuild = func() { // an agent writes while committing
		e.codec.onBuild = nil
		if _, err := e.svc.WriteItems(e.ctx, "root", "agent:laptop", "apply",
			[]Write{{Ref: other, Value: map[string]any{"name": "support"}, Previous: workspace.Missing}},
			&workspace.Note{Source: "agent", By: "laptop", Text: "support"}); err != nil {
			t.Error(err)
		}
	}
	rev, err := e.svc.Commit(e.ctx, "root", "root", CommitRequest{Version: st.Workspace.Version, ExpectedPublished: 1,
		Comment: "mine"})
	if err != nil || rev.State != revision.StatePublished {
		t.Fatalf("commit = %+v %v", rev, err)
	}
	after := e.read()
	if after.Workspace.Base != rev.ID || workspace.IsMissing(after.Workspace.Items.Get(other)) ||
		len(schema.Changes(after.Base, after.Workspace.Items)) != 1 || len(after.Workspace.Notes) != 1 {
		t.Fatalf("after commit = %+v", after.Workspace)
	}
	if got, _ := e.revs.Get(e.ctx, cp.ID); got.State != revision.StateDiscarded {
		t.Fatalf("the checkpoint is %s", got.State)
	}
}

// failingStore fails to write while fail is set.
type failingStore struct {
	workspace.Store
	fail *bool
}

func (s failingStore) PutWorkspace(
	ctx context.Context, w workspace.Workspace, expected int64, entry workspace.LogEntry,
) (workspace.Workspace, error) {
	if *s.fail {
		return workspace.Workspace{}, errors.New("store down")
	}
	return s.Store.PutWorkspace(ctx, w, expected, entry)
}

// A commit published whose workspace cannot then be moved onto it says so,
// with the revision: it is published, and the workspace still builds on the
// one before.
func TestCommitPublishedButNotMoved(t *testing.T) {
	e := newEnv(t)
	st := e.write("root", role("mine"), role("base"))
	fail := false
	e.svc.Store = failingStore{e.svc.Store, &fail}
	e.codec.onBuild = func() { fail = true }
	rev, err := e.svc.Commit(e.ctx, "root", "root", CommitRequest{Version: st.Workspace.Version, ExpectedPublished: 1})
	var notMoved *NotMovedError
	if !errors.As(err, &notMoved) || notMoved.Published.ID != rev.ID || rev.State != revision.StatePublished {
		t.Fatalf("commit = %+v, %v", rev, err)
	}
	if published, _ := e.revs.Published(e.ctx); published.ID != rev.ID {
		t.Fatalf("published = #%d, want #%d", published.ID, rev.ID)
	}
	fail = false
	if w := e.read().Workspace; w.Base != 1 {
		t.Fatalf("the workspace moved: %+v", w)
	}
}

// A commit the published revision moved under fails, storing no draft.
func TestCommitBehindThePublishedRevision(t *testing.T) {
	e := newEnv(t)
	st := e.write("root", role("mine"), role("base"))
	e.publish(`[]`)
	before, _ := e.revs.List(e.ctx, 0)
	var conflict *revisionops.ConflictError
	if _, err := e.svc.Commit(e.ctx, "root", "root", CommitRequest{Version: st.Workspace.Version,
		ExpectedPublished: 1}); !errors.As(err, &conflict) || conflict.Published != 2 {
		t.Fatalf("committed behind the published revision: %v", err)
	}
	// Refused before a draft was stored.
	if after, _ := e.revs.List(e.ctx, 0); len(after) != len(before) {
		t.Fatalf("revisions %d, then %d", len(before), len(after))
	}
	if _, err := e.svc.Commit(e.ctx, "root", "root", CommitRequest{Version: st.Workspace.Version,
		ExpectedPublished: 2}); !errors.Is(err, ErrBehind) {
		t.Fatalf("committed a workspace built on an older revision: %v", err)
	}
}

func TestLog(t *testing.T) {
	e := newEnv(t)
	e.write("root", role("mine"), role("base"))
	e.write("agent:laptop", role("agent"), role("mine"))
	log, err := e.svc.Log(e.ctx, "root", 0)
	if err != nil || len(log) != 2 || log[0].By != "agent:laptop" || log[0].Items[0] != analyst || log[1].By != "root" {
		t.Fatalf("log = %+v %v", log, err)
	}
}

// A write renaming an item replaces the one it came from, and what Reconcile
// derives from it lands in the same version, logged with the write.
func TestWriteReconcilesRenames(t *testing.T) {
	e := newEnv(t)
	lead := workspace.Ref{Kind: "role", Key: "lead"}
	note := workspace.Ref{Kind: "note", Key: "renames"}
	var gotBefore, gotAfter workspace.Snapshot
	var gotRenames map[workspace.Ref]workspace.Ref
	e.svc.Reconcile = func(before, after workspace.Snapshot, renames map[workspace.Ref]workspace.Ref) (
		workspace.Snapshot, error,
	) {
		gotBefore, gotAfter, gotRenames = before, after, renames
		return after.With(note, map[string]any{"name": "renames", "count": len(renames)}), nil
	}
	renamed := map[string]any{"name": "lead", "grants": []any{}}
	st, err := e.svc.WriteItems(e.ctx, "root", "root", "edit", []Write{
		{Ref: lead, Value: renamed, Previous: workspace.Missing, From: &analyst},
		{Ref: analyst, Value: workspace.Missing, Previous: role("base")},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if workspace.IsMissing(gotBefore.Get(analyst)) || !workspace.IsMissing(gotAfter.Get(analyst)) ||
		gotRenames[lead] != analyst {
		t.Fatalf("reconcile saw before %v, after %v, renames %v", gotBefore, gotAfter, gotRenames)
	}
	if st.Workspace.Version != 1 || !workspace.IsMissing(st.Workspace.Items.Get(analyst)) ||
		workspace.IsMissing(st.Workspace.Items.Get(note)) {
		t.Fatalf("workspace = %+v", st.Workspace)
	}
	log, err := e.svc.Log(e.ctx, "root", 0)
	if err != nil || len(log) != 1 || !slices.Contains(log[0].Items, analyst) || !slices.Contains(log[0].Items, note) {
		t.Fatalf("log = %+v %v", log, err)
	}
}

// A failing Reconcile writes nothing.
func TestWriteFailsWithReconcile(t *testing.T) {
	e := newEnv(t)
	e.svc.Reconcile = func(_, _ workspace.Snapshot, _ map[workspace.Ref]workspace.Ref) (workspace.Snapshot, error) {
		return nil, errors.New("inconsistent")
	}
	if _, err := e.svc.WriteItems(e.ctx, "root", "root", "edit",
		[]Write{{Ref: analyst, Value: role("mine"), Previous: role("base")}}, nil); err == nil {
		t.Fatal("wrote despite a failing reconcile")
	}
	if e.read().Workspace.Version != 0 {
		t.Fatal("a failed write was kept")
	}
}

// A rename written as the console writes it, with the removal of the item it
// renames, merges like any write: a change an agent made meanwhile to the
// renamed item is a conflict, never dropped silently.
func TestRenameKeepsAChangeMadeMeanwhile(t *testing.T) {
	e := newEnv(t)
	read := role("base")
	e.write("agent:bot", role("base", "orders"), read)
	lead := workspace.Ref{Kind: "role", Key: "lead"}
	renamed := map[string]any{"name": "lead", "description": "base", "grants": []any{}}
	st, err := e.svc.WriteItems(e.ctx, "root", "root", "edit", []Write{
		{Ref: lead, Value: renamed, Previous: workspace.Missing, From: &analyst},
		{Ref: analyst, Value: workspace.Missing, Previous: read},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Open) != 1 || st.Open[0].Ref != analyst || workspace.IsMissing(st.Open[0].TheirsValue) {
		t.Fatalf("the agent's change was dropped: conflicts %+v", st.Open)
	}
	if workspace.IsMissing(st.Workspace.Items.Get(lead)) {
		t.Fatal("the renamed item was not written")
	}
}

// A rename written alone, without the removal of the item it renames, is
// refused while that item is there: what its writer read of it is not known,
// so a change made to it meanwhile would be lost.
func TestARenameAloneIsRefused(t *testing.T) {
	e := newEnv(t)
	lead := workspace.Ref{Kind: "role", Key: "lead"}
	renamed := map[string]any{"name": "lead", "description": "base", "grants": []any{}}
	_, err := e.svc.WriteItems(e.ctx, "root", "root", "edit",
		[]Write{{Ref: lead, Value: renamed, Previous: workspace.Missing, From: &analyst}}, nil)
	if !errors.Is(err, ErrRenameAlone) || e.read().Workspace.Version != 0 {
		t.Fatalf("a rename alone: %v", err)
	}
}

// A rename written again (a retry after its answer was lost) is a write
// alone: the item it came from is gone already, so Reconcile is told of no
// rename, which would read the item now at the new name as replaced.
func TestARenameWrittenAgainIsAWriteAlone(t *testing.T) {
	e := newEnv(t)
	lead := workspace.Ref{Kind: "role", Key: "lead"}
	var renames []map[workspace.Ref]workspace.Ref
	e.svc.Reconcile = func(_, after workspace.Snapshot, r map[workspace.Ref]workspace.Ref) (workspace.Snapshot, error) {
		renames = append(renames, r)
		return after, nil
	}
	renamed := map[string]any{"name": "lead", "description": "base", "grants": []any{}}
	for range 2 {
		if _, err := e.svc.WriteItems(e.ctx, "root", "root", "edit", []Write{
			{Ref: lead, Value: renamed, Previous: workspace.Missing, From: &analyst},
			{Ref: analyst, Value: workspace.Missing, Previous: role("base")},
		}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(renames) != 2 || renames[0][lead] != analyst || len(renames[1]) != 0 {
		t.Fatalf("renames = %v", renames)
	}
	if workspace.IsMissing(e.read().Workspace.Items.Get(lead)) {
		t.Fatal("the renamed item is gone")
	}
}

// Two items renamed to each other's names in one write swap: both stay, and
// Reconcile is told of both renames.
func TestRenamesSwappingNames(t *testing.T) {
	e := newEnv(t)
	lead := workspace.Ref{Kind: "role", Key: "lead"}
	leadRole := map[string]any{"name": "lead", "description": "lead", "grants": []any{}}
	if _, err := e.svc.WriteItems(e.ctx, "root", "root", "edit",
		[]Write{{Ref: lead, Value: leadRole, Previous: workspace.Missing}}, nil); err != nil {
		t.Fatal(err)
	}
	var renames map[workspace.Ref]workspace.Ref
	e.svc.Reconcile = func(_, after workspace.Snapshot, r map[workspace.Ref]workspace.Ref) (workspace.Snapshot, error) {
		renames = r
		return after, nil
	}
	analystRole := e.analyst()
	asLead := map[string]any{"name": "lead", "description": "base", "grants": []any{}}
	asAnalyst := map[string]any{"name": "analyst", "description": "lead", "grants": []any{}}
	st, err := e.svc.WriteItems(e.ctx, "root", "root", "edit", []Write{
		{Ref: lead, Value: asLead, Previous: leadRole, From: &analyst},
		{Ref: analyst, Value: asAnalyst, Previous: analystRole, From: &lead},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	items := st.Workspace.Items
	if !schema.Same(items.Get(lead), asLead) || !schema.Same(items.Get(analyst), asAnalyst) || len(st.Open) != 0 {
		t.Fatalf("items = %v, conflicts %+v", items, st.Open)
	}
	if renames[lead] != analyst || renames[analyst] != lead {
		t.Fatalf("renames = %v", renames)
	}
}

// Renames chained in one write (analyst to lead, lead to viewer) move the
// item on: the key between goes, and Reconcile is told of one rename, from
// where the item came.
func TestRenamesChainedInOneWrite(t *testing.T) {
	e := newEnv(t)
	lead := workspace.Ref{Kind: "role", Key: "lead"}
	viewer := workspace.Ref{Kind: "role", Key: "viewer"}
	var renames map[workspace.Ref]workspace.Ref
	e.svc.Reconcile = func(_, after workspace.Snapshot, r map[workspace.Ref]workspace.Ref) (workspace.Snapshot, error) {
		renames = r
		return after, nil
	}
	asLead := map[string]any{"name": "lead", "description": "base", "grants": []any{}}
	asViewer := map[string]any{"name": "viewer", "description": "base", "grants": []any{}}
	st, err := e.svc.WriteItems(e.ctx, "root", "root", "edit", []Write{
		{Ref: lead, Value: asLead, Previous: workspace.Missing, From: &analyst},
		{Ref: viewer, Value: asViewer, Previous: workspace.Missing, From: &lead},
		{Ref: analyst, Value: workspace.Missing, Previous: role("base")},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	items := st.Workspace.Items
	if !workspace.IsMissing(items.Get(analyst)) || !workspace.IsMissing(items.Get(lead)) ||
		!schema.Same(items.Get(viewer), asViewer) || len(st.Open) != 0 {
		t.Fatalf("items = %v, conflicts %+v", items, st.Open)
	}
	if len(renames) != 1 || renames[viewer] != analyst {
		t.Fatalf("renames = %v", renames)
	}
}
