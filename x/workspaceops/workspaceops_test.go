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

var schema = workspace.Schema{ListKeys: []string{"name", "entity"}, OptionalText: []string{"description"}}

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
