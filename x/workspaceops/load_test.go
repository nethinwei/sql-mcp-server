package workspaceops

import (
	"errors"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/core/workspace"
)

// A workspace builds on a published revision: one never published (a draft,
// discarded or not) is loaded or merged, not built on.
func TestOnlyAPublishedRevisionIsABase(t *testing.T) {
	e := newEnv(t)
	draft, err := e.revs.Create(e.ctx, revision.Draft{ParentID: 1, Payload: []byte(items(role("draft")))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Reset(e.ctx, "root", "root", draft.ID); !errors.Is(err, ErrNeverPublished) {
		t.Fatalf("reset onto a draft = %v", err)
	}
	if _, err := e.revs.Discard(e.ctx, draft.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Update(e.ctx, "root", "root", draft.ID); !errors.Is(err, ErrNeverPublished) {
		t.Fatalf("update onto a discarded draft = %v", err)
	}
	if st := e.read(); st.Workspace.Base != 1 {
		t.Fatalf("base = %d", st.Workspace.Base)
	}
}

// Loading a revision, an older one or a discarded draft, makes its content
// changes to the published revision: committed, they restore it.
func TestLoadedRevisionCommits(t *testing.T) {
	e := newEnv(t)
	e.publish(items(role("second")))
	draft, err := e.revs.Create(e.ctx, revision.Draft{ParentID: 2, Payload: []byte(items(role("discarded")))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.revs.Discard(e.ctx, draft.ID); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		id   int64
		want string
	}{{1, "base"}, {draft.ID, "discarded"}} {
		e.write("root", role("mine"), nil)
		st, err := e.svc.Load(e.ctx, "root", "root", c.id, false)
		if err != nil {
			t.Fatal(err)
		}
		published, _ := e.revs.Published(e.ctx)
		if st.Workspace.Base != published.ID || e.analyst()["description"] != c.want {
			t.Fatalf("load #%d: base %d, analyst %v", c.id, st.Workspace.Base, e.analyst())
		}
		rev, err := e.svc.Commit(e.ctx, "root", "root", CommitRequest{Version: st.Workspace.Version,
			ExpectedPublished: published.ID})
		if err != nil {
			t.Fatalf("commit after loading #%d: %v", c.id, err)
		}
		got, _ := (&codec{}).Items(rev)
		if got.Get(analyst).(map[string]any)["description"] != c.want {
			t.Fatalf("committed %v", got)
		}
	}
}

// Loading keeping what was changed re-applies it onto the revision loaded:
// a value both changed differently keeps the workspace's, as a conflict
// holding the revision's, and the drafts taken in stay taken.
func TestLoadKeepingChanges(t *testing.T) {
	e := newEnv(t)
	e.publish(items(role("second", "orders")))
	e.write("root", role("mine", "orders"), role("second", "orders"))
	st, err := e.svc.Load(e.ctx, "root", "root", 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if st.Workspace.Base != 2 || e.analyst()["description"] != "mine" || len(e.analyst()["grants"].([]any)) != 0 {
		t.Fatalf("loaded = %+v", e.analyst())
	}
	if len(st.Open) != 1 || !st.Open[0].Pinned || st.Open[0].Source != (workspace.Source{
		Kind: workspace.SourcePublished, ID: "1"}) {
		t.Fatalf("open = %+v", st.Open)
	}
}
