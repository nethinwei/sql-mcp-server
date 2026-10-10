package workspaceops

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/core/workspace"
)

// A model test: random operations on one workspace, some of them run while
// a commit or a checkpoint is building (as another console or an agent
// would), checking after each step what the workspace records of drafts and
// checkpoints, and what a commit publishes. Seeded, so a failure names a
// seed that reproduces it.

type model struct {
	*env
	r    *rng
	seed uint32
	// merged is the workspace version each draft was taken in at.
	merged map[int64]int64
	// committed is the version of the snapshot each commit published.
	committed []int64
	// dropped is the workspace version a reset dropped each draft taken in at.
	dropped map[int64]int64
	log     []string
}

type rng struct{ s uint32 }

func (r *rng) next() int {
	r.s = r.s*1664525 + 1013904223
	return int(r.s >> 8)
}

func (r *rng) intn(n int) int { return r.next() % n }

var keys = []string{"r0", "r1", "r2"}

func roleItem(key, description string) any {
	return map[string]any{"name": key, "description": description}
}

// write writes one item, from what was read or (sometimes) from a stale read.
func (m *model) write() {
	ref := workspace.Ref{Kind: "role", Key: keys[m.r.intn(len(keys))]}
	previous := m.read().Workspace.Items.Get(ref)
	if m.r.intn(4) == 0 {
		previous = workspace.Missing
	}
	value := roleItem(ref.Key, fmt.Sprintf("w%d", m.r.intn(5)))
	m.log = append(m.log, fmt.Sprintf("write %s", ref.Key))
	if _, err := m.svc.WriteItems(m.ctx, "root", "agent:a", "apply", []Write{{Ref: ref, Value: value,
		Previous: previous}}, nil); err != nil {
		m.t.Fatalf("seed %d: write: %v", m.seed, err)
	}
}

// merge takes in a new draft of the published revision changing one item.
func (m *model) merge() {
	published, _ := m.revs.Published(m.ctx)
	items, _ := (codec{}).Items(published)
	key := keys[m.r.intn(len(keys))]
	items = items.With(workspace.Ref{Kind: "role", Key: key}, roleItem(key, fmt.Sprintf("d%d", m.r.intn(5))))
	payload, _ := json.Marshal(items)
	draft, err := m.revs.Create(m.ctx, revision.Draft{ParentID: published.ID, Payload: payload, Author: "alice"})
	if err != nil {
		m.t.Fatal(err)
	}
	st, err := m.svc.MergeDraft(m.ctx, "root", "root", draft.ID)
	if err != nil {
		m.t.Fatalf("seed %d: merge: %v", m.seed, err)
	}
	m.merged[draft.ID] = st.Workspace.Version
	m.log = append(m.log, fmt.Sprintf("merge #%d at v%d", draft.ID, st.Workspace.Version))
}

// publishOther publishes a change of one item made elsewhere (another
// administrator); update then moves the workspace onto it.
func (m *model) publishOther() {
	published, _ := m.revs.Published(m.ctx)
	items, _ := (codec{}).Items(published)
	key := keys[m.r.intn(len(keys))]
	items = items.With(workspace.Ref{Kind: "role", Key: key}, roleItem(key, fmt.Sprintf("p%d", m.r.intn(5))))
	payload, _ := json.Marshal(items)
	id := m.publish(string(payload))
	m.log = append(m.log, fmt.Sprintf("published #%d elsewhere", id))
}

func (m *model) update() {
	published, _ := m.revs.Published(m.ctx)
	m.log = append(m.log, fmt.Sprintf("update #%d", published.ID))
	if _, err := m.svc.Update(m.ctx, "root", "root", published.ID); err != nil {
		m.t.Fatalf("seed %d: update: %v", m.seed, err)
	}
}

func (m *model) reset() {
	published, _ := m.revs.Published(m.ctx)
	m.log = append(m.log, fmt.Sprintf("reset #%d", published.ID))
	st, err := m.svc.Reset(m.ctx, "root", "root", published.ID)
	if err != nil {
		m.t.Fatalf("seed %d: reset: %v", m.seed, err)
	}
	for id := range m.merged {
		if _, done := m.dropped[id]; !done && !m.published(id) {
			m.dropped[id] = st.Workspace.Version
		}
	}
}

// published is whether a commit's snapshot held draft (taken in before it,
// and not dropped by a reset before it).
func (m *model) published(draft int64) bool {
	at, dropped := m.merged[draft], m.dropped[draft]
	return slices.ContainsFunc(m.committed, func(v int64) bool { return v >= at && (dropped == 0 || v < dropped) })
}

func (m *model) resolve() {
	if open := m.read().Open; len(open) > 0 {
		m.log = append(m.log, "resolve")
		if _, err := m.svc.Resolve(m.ctx, "root", "root", open[0].ID, m.r.intn(2) == 0); err != nil {
			m.t.Fatalf("seed %d: resolve: %v", m.seed, err)
		}
	}
}

// meanwhile runs 0-2 operations while a commit or a checkpoint builds.
func (m *model) meanwhile() {
	m.codec.onBuild = func() {
		m.codec.onBuild = nil
		for n := m.r.intn(3); n > 0; n-- {
			m.log = append(m.log, "  meanwhile:")
			m.step(false)
		}
	}
}

func (m *model) checkpoint() {
	m.log = append(m.log, "checkpoint")
	m.meanwhile()
	if _, err := m.svc.Checkpoint(m.ctx, "root", "root", ""); err != nil && !errors.Is(err, ErrConflicts) {
		m.t.Fatalf("seed %d: checkpoint: %v", m.seed, err)
	}
	m.codec.onBuild = nil
}

func (m *model) commit() {
	st := m.read()
	m.log = append(m.log, fmt.Sprintf("commit v%d", st.Workspace.Version))
	m.meanwhile()
	rev, err := m.svc.Commit(m.ctx, "root", "root", CommitRequest{Version: st.Workspace.Version,
		ExpectedPublished: st.Workspace.Base})
	m.codec.onBuild = nil
	// Conflicts to resolve, or published elsewhere since: update first.
	if errors.Is(err, ErrConflicts) || errors.Is(err, revision.ErrConflict) {
		return
	}
	if err != nil {
		m.t.Fatalf("seed %d: commit: %v", m.seed, err)
	}
	// What is published is the snapshot reviewed.
	want, _ := json.Marshal(st.Workspace.Items)
	if string(rev.Payload) != string(want) {
		m.fail("published %s, reviewed %s", rev.Payload, want)
	}
	m.committed = append(m.committed, st.Workspace.Version)
}

// step runs one random operation; commits only at the top level.
func (m *model) step(top bool) {
	switch n := m.r.intn(13); {
	case n < 4:
		m.write()
	case n < 6:
		m.merge()
	case n < 7:
		m.resolve()
	case n < 8:
		m.publishOther()
	case n < 9:
		m.update()
	case n < 10 || !top:
		m.checkpoint()
	case n < 11:
		m.reset()
	default:
		m.commit()
	}
}

func (m *model) fail(format string, args ...any) {
	m.t.Helper()
	m.t.Fatalf("seed %d: %s\n%s", m.seed, fmt.Sprintf(format, args...), slices.Concat([]string{"steps:"}, m.log))
}

// check checks what the workspace records of drafts and its checkpoint.
func (m *model) check() {
	m.t.Helper()
	w := m.read().Workspace
	for id, at := range m.merged {
		rev, _ := m.revs.Get(m.ctx, id)
		published := m.published(id)
		switch {
		case m.dropped[id] != 0 && !published:
			// Dropped by a reset: a draft still, offered again.
			if rev.State != revision.StateDraft || slices.Contains(w.Taken, id) {
				m.fail("draft #%d dropped by a reset: %s, taken %v", id, rev.State, w.Taken)
			}
		case published && (rev.State != revision.StateDiscarded || slices.Contains(w.Taken, id)):
			m.fail("draft #%d taken at v%d and published: %s, taken %v", id, at, rev.State, w.Taken)
		case !published && (rev.State != revision.StateDraft || !slices.Contains(w.Taken, id)):
			m.fail("draft #%d taken at v%d, not published: %s, taken %v", id, at, rev.State, w.Taken)
		}
	}
	if w.Checkpoint != 0 && w.CheckpointAt == w.Version {
		rev, _ := m.revs.Get(m.ctx, w.Checkpoint)
		if want, _ := json.Marshal(w.Items); string(rev.Payload) != string(want) || rev.State != revision.StateDraft {
			m.fail("checkpoint #%d (%s) holds %s, the workspace %s", rev.ID, rev.State, rev.Payload, want)
		}
	}
}

func TestInterleavedOperations(t *testing.T) {
	for seed := uint32(1); seed <= 400; seed++ {
		e := newEnv(t)
		m := &model{env: e, r: &rng{seed}, seed: seed, merged: map[int64]int64{}, dropped: map[int64]int64{}}
		for range 30 {
			m.step(true)
			m.check()
		}
	}
}
