package configapp

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/core/workspace"
	"github.com/nethinwei/sql-mcp-server/internal/kit"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/configplan"
	"github.com/nethinwei/sql-mcp-server/x/workspaceops"
)

// An administrator's workspace (see core/workspace and x/workspaceops) is
// the one copy of their unpublished work: they (in any console) and their
// agents (their tokens) write it, and they commit it. Here are the
// configuration's side of it (its codec and merge rules) and the checks of
// who may do what.

// ErrWorkspaceChanged: the workspace was written since it was read.
var ErrWorkspaceChanged = workspace.ErrChanged

func (r *Service) workspaces() (workspaceops.Service, error) {
	store, err := r.plans()
	if err != nil {
		return workspaceops.Service{}, err
	}
	return workspaceops.Service{Store: store, Codec: codec{r}, Schema: Schema, Revisions: r.Store,
		History: r.Ops(), Reconcile: reconcile, Cache: &r.items}, nil
}

func toWorkspace(st workspaceops.State) (*Workspace, error) {
	w := st.Workspace
	out := &Workspace{Base: strconv.FormatInt(w.Base, 10), Version: int(w.Version), UpdatedBy: w.UpdatedBy,
		Conflicts: []WorkspaceConflict{}, Notes: []WorkspaceNote{}, items: w.Items, username: w.Username}
	if !w.UpdatedAt.IsZero() {
		out.UpdatedAt = &w.UpdatedAt
	}
	for _, c := range st.Open {
		out.Conflicts = append(out.Conflicts, toConflict(c))
	}
	for _, n := range w.Notes {
		out.Notes = append(out.Notes, WorkspaceNote(n))
	}
	out.Taken = make([]string, 0, len(w.Taken))
	for _, id := range w.Taken {
		out.Taken = append(out.Taken, strconv.FormatInt(id, 10))
	}
	if w.Checkpoint != 0 && w.CheckpointAt == w.Version {
		out.Checkpoint = kit.Optional(strconv.FormatInt(w.Checkpoint, 10))
	}
	return out, nil
}

// RevisionSections are revision id's configuration as the console's
// sections (the workspace's items), decoded once per revision: load reads
// the revision when they are not kept.
func (r *Service) RevisionSections(id int64, load func() (revision.Revision, error)) (any, error) {
	items, err := r.items.Items(id, func() (workspace.Snapshot, error) {
		rev, err := load()
		if err != nil {
			return nil, err
		}
		return codec{r}.Items(rev)
	})
	if err != nil {
		return nil, err
	}
	return rawSections(items)
}

// Warm reads the published revision's items and configuration into the
// caches, which the first console to load after a start would otherwise
// wait for (a large part of a second for thousands of entities).
func (r *Service) Warm(ctx context.Context) {
	rev, err := r.Store.Published(ctx)
	if err != nil {
		return
	}
	load := func() (revision.Revision, error) { return rev, nil }
	_, _ = r.items.Items(rev.ID, func() (workspace.Snapshot, error) { return codec{r}.Items(rev) })
	_, _, _ = r.RevisionSpec(rev.ID, load)
}

// Sections are the workspace's items as the console's sections.
func (w *Workspace) Sections() (any, error) {
	return rawSections(w.items)
}

// deltaOps are the operations whose log entry lists every item they changed;
// the others (update, reset, commit) move the workspace onto another
// revision, and list what it changed from its base.
var deltaOps = map[string]bool{"edit": true, "apply": true, "merge": true, "resolve": true, "checkpoint": true}

// WorkspaceItems are w's items changed since version since, as its log
// tells them, or all of them when since is nil or the log cannot tell.
func (r *Service) WorkspaceItems(ctx context.Context, w *Workspace, since *int) (*WorkspaceItems, error) {
	if since != nil && *since >= 0 && *since <= w.Version && w.username != "" {
		refs, ok, err := r.changedSince(ctx, w.username, *since, w.Version)
		if err != nil {
			return nil, err
		}
		if ok {
			return &WorkspaceItems{Items: itemsAt(w.items, refs)}, nil
		}
	}
	out := &WorkspaceItems{Complete: true, Items: make([]WorkspaceItem, 0, len(w.items))}
	for _, it := range w.items {
		out.Items = append(out.Items, WorkspaceItem{Kind: it.Kind, Key: it.Key, Value: it.Value})
	}
	return out, nil
}

// maxDeltaWrites bounds the writes a delta is read from; a console further
// behind reads the workspace whole.
const maxDeltaWrites = 200

// changedSince are the items the writes after version since, up to version,
// changed; false when one of them is not in the log or does not list them.
func (r *Service) changedSince(ctx context.Context, username string, since, version int) (
	[]workspace.Ref, bool, error,
) {
	if since == version {
		return nil, true, nil
	}
	if version-since > maxDeltaWrites {
		return nil, false, nil // reading that many writes costs more than every item
	}
	ops, err := r.workspaces()
	if err != nil {
		return nil, false, err
	}
	// Newest first; writes after version (made meanwhile) come first.
	log, err := ops.Log(ctx, username, version-since+16)
	if err != nil {
		return nil, false, err
	}
	var refs []workspace.Ref
	seen, next := map[workspace.Ref]bool{}, version
	for _, e := range log {
		if e.Version > int64(version) {
			continue
		}
		if e.Version != int64(next) || !deltaOps[e.Op] {
			return nil, false, nil
		}
		for _, ref := range e.Items {
			if !seen[ref] {
				seen[ref] = true
				refs = append(refs, ref)
			}
		}
		if next--; next == since {
			return refs, true, nil
		}
	}
	return nil, false, nil
}

// itemsAt are the items refs name in s, in s's order, then those s no
// longer holds (removed: nil values).
func itemsAt(s workspace.Snapshot, refs []workspace.Ref) []WorkspaceItem {
	want := make(map[workspace.Ref]bool, len(refs))
	for _, ref := range refs {
		want[ref] = true
	}
	out := make([]WorkspaceItem, 0, len(refs))
	for _, it := range s {
		if want[it.Ref] {
			out = append(out, WorkspaceItem{Kind: it.Kind, Key: it.Key, Value: it.Value})
			delete(want, it.Ref)
		}
	}
	for _, ref := range refs {
		if want[ref] {
			out = append(out, WorkspaceItem{Kind: ref.Kind, Key: ref.Key})
		}
	}
	return out
}

func toConflict(c workspace.Open) WorkspaceConflict {
	wc := WorkspaceConflict{ID: c.ID, Kind: c.Ref.Kind, Key: c.Ref.Key, Path: c.Path.String(),
		Mine: c.Mine, MineGone: workspace.IsMissing(c.Mine), Theirs: c.TheirsValue,
		TheirsGone: workspace.IsMissing(c.TheirsValue), Source: c.Source.Kind,
		FromID: kit.Optional(c.Source.ID), FromBy: kit.Optional(c.Source.By)}
	if wc.MineGone {
		wc.Mine = nil
	}
	if wc.TheirsGone {
		wc.Theirs = nil
	}
	return wc
}

// workspaceOp runs fn on the caller's workspace with permission perm.
func (r *Service) workspaceOp(ctx context.Context, perm string,
	fn func(ops workspaceops.Service, username string) (workspaceops.State, error),
) (*Workspace, error) {
	who, err := auth.RequireUnlimited(ctx, perm)
	if err != nil {
		return nil, err
	}
	ops, err := r.workspaces()
	if err != nil {
		return nil, err
	}
	st, err := fn(ops, who.Username)
	if err != nil {
		return nil, err
	}
	return toWorkspace(st)
}

// Workspace is the caller's workspace; nil when nothing is published.
func (r *Service) Workspace(ctx context.Context) (*Workspace, error) {
	out, err := r.workspaceOp(
		ctx,
		auth.PermRead,
		func(ops workspaceops.Service, user string) (workspaceops.State, error) {
			st, ok, err := ops.Read(ctx, user)
			if err == nil && !ok {
				err = workspaceops.ErrNothingPublished
			}
			return st, err
		},
	)
	if errors.Is(err, workspaceops.ErrNothingPublished) {
		return nil, nil
	}
	return out, err
}

// EditWorkspace writes items of the caller's workspace (see
// workspaceops.WriteItems: an item written meanwhile merges).
// The items are read only once the caller may write.
func (r *Service) EditWorkspace(ctx context.Context, in WorkspaceEditInput) (*Workspace, error) {
	return r.workspaceOp(ctx, auth.PermWrite, func(ops workspaceops.Service, user string) (workspaceops.State, error) {
		writes, err := workspaceWrites(in)
		if err != nil {
			return workspaceops.State{}, err
		}
		return ops.WriteItems(ctx, user, user, "edit", writes, nil)
	})
}

// workspaceWrites are the writes in names, checked.
func workspaceWrites(in WorkspaceEditInput) ([]workspaceops.Write, error) {
	writes := make([]workspaceops.Write, 0, len(in.Items))
	for _, it := range in.Items {
		ref := workspace.Ref{Kind: it.Kind, Key: it.Key}
		value, previous := orMissing(it.Value), orMissing(it.Previous)
		if err := checkKey(ref, value); err != nil {
			return nil, err
		}
		write := workspaceops.Write{Ref: ref, Value: value, Previous: previous}
		if it.RenamedFrom != nil {
			if ref.Kind != ItemEntity && ref.Kind != ItemRole {
				return nil, errors.New("only an entity or a role is renamed (renamedFrom)")
			}
			write.From = &workspace.Ref{Kind: ref.Kind, Key: *it.RenamedFrom}
		}
		writes = append(writes, write)
	}
	return writes, nil
}

// orMissing is a GraphQL JSON value as an item value (numbers as decoding
// JSON makes them): null is Missing.
func orMissing(v any) any {
	if v == nil {
		return workspace.Missing
	}
	data, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if json.Unmarshal(data, &out) != nil {
		return v
	}
	return out
}

// checkKey checks that ref names an item of a known kind (the one settings
// item is named settings) and value is that item (renaming removes one item
// and writes another).
func checkKey(ref workspace.Ref, value any) error {
	switch ref.Kind {
	case ItemEntity, ItemRole, ItemUser:
	case ItemSettings:
		if ref != settingsRef {
			return errors.New("the settings item is named " + strconv.Quote(ItemSettings))
		}
		return nil
	default:
		return errors.New("unknown workspace item kind " + strconv.Quote(ref.Kind))
	}
	if workspace.IsMissing(value) {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if key, err := itemKey(ref.Kind, raw); err != nil || key != ref.Key {
		return errors.New("the " + ref.Kind + " written is not " + strconv.Quote(ref.Key) +
			" (a rename writes the new item with renamedFrom)")
	}
	return nil
}

// UpdateWorkspace moves the caller's workspace onto revision to.
func (r *Service) UpdateWorkspace(ctx context.Context, to string) (*Workspace, error) {
	return r.workspaceOp(ctx, auth.PermWrite, func(ops workspaceops.Service, user string) (workspaceops.State, error) {
		id, err := ParseID(to)
		if err != nil {
			return workspaceops.State{}, err
		}
		return ops.Update(ctx, user, user, id)
	})
}

// ResetWorkspace makes the caller's workspace revision to as it is.
func (r *Service) ResetWorkspace(ctx context.Context, to string) (*Workspace, error) {
	return r.workspaceOp(ctx, auth.PermWrite, func(ops workspaceops.Service, user string) (workspaceops.State, error) {
		id, err := ParseID(to)
		if err != nil {
			return workspaceops.State{}, err
		}
		return ops.Reset(ctx, user, user, id)
	})
}

// MergeDraft takes draft id into the caller's workspace.
func (r *Service) MergeDraft(ctx context.Context, id string) (*Workspace, error) {
	return r.workspaceOp(ctx, auth.PermWrite, func(ops workspaceops.Service, user string) (workspaceops.State, error) {
		draft, err := ParseID(id)
		if err != nil {
			return workspaceops.State{}, err
		}
		return ops.MergeDraft(ctx, user, user, draft)
	})
}

// ResolveConflict resolves a conflict of the caller's workspace.
func (r *Service) ResolveConflict(ctx context.Context, id string, theirs bool) (*Workspace, error) {
	return r.workspaceOp(ctx, auth.PermWrite, func(ops workspaceops.Service, user string) (workspaceops.State, error) {
		return ops.Resolve(ctx, user, user, id, theirs)
	})
}

// CheckpointWorkspace saves the caller's workspace as a draft to share.
func (r *Service) CheckpointWorkspace(ctx context.Context, comment *string) (revision.Revision, error) {
	who, err := auth.RequireUnlimited(ctx, auth.PermWrite)
	if err != nil {
		return revision.Revision{}, err
	}
	ops, err := r.workspaces()
	if err != nil {
		return revision.Revision{}, err
	}
	return ops.Checkpoint(ctx, who.Username, who.Username, strings.TrimSpace(kit.Deref(comment)))
}

// CommitWorkspace publishes the caller's workspace at the version reviewed.
func (r *Service) CommitWorkspace(ctx context.Context, in CommitInput) (revision.Revision, error) {
	who, err := auth.RequireUnlimited(ctx, auth.PermPublish)
	if err != nil {
		return revision.Revision{}, err
	}
	expected, err := ParseID(in.ExpectedPublished)
	if err != nil {
		return revision.Revision{}, err
	}
	ops, err := r.workspaces()
	if err != nil {
		return revision.Revision{}, err
	}
	return ops.Commit(ctx, who.Username, who.Username, workspaceops.CommitRequest{Version: int64(in.Version),
		ExpectedPublished: expected, RestartRequired: kit.DerefBool(in.RestartRequired),
		Comment: strings.TrimSpace(kit.Deref(in.Comment))})
}

// WorkspaceLog is the caller's workspace log, newest first.
func (r *Service) WorkspaceLog(ctx context.Context, limit *int) ([]WorkspaceLogEntry, error) {
	who, err := auth.RequireUnlimited(ctx, auth.PermRead)
	if err != nil {
		return nil, err
	}
	ops, err := r.workspaces()
	if err != nil {
		return nil, err
	}
	log, err := ops.Log(ctx, who.Username, kit.DerefInt(limit, 100))
	if err != nil {
		return nil, err
	}
	out := make([]WorkspaceLogEntry, 0, len(log))
	for _, e := range log {
		items := make([]WorkspaceRef, 0, len(e.Items))
		for _, ref := range e.Items {
			items = append(items, WorkspaceRef(ref))
		}
		out = append(out, WorkspaceLogEntry{Version: int(e.Version), At: e.At, By: e.By, Op: e.Op, Items: items,
			Note: kit.Optional(e.Note)})
	}
	return out, nil
}

// workspaceDraft is the caller's workspace as a draft, with its state; nil
// (the published configuration) when nothing is published.
func (r *Service) workspaceDraft(ctx context.Context, username string) (*Draft, workspaceops.State, error) {
	ops, err := r.workspaces()
	if err != nil {
		return nil, workspaceops.State{}, err
	}
	st, ok, err := ops.Read(ctx, username)
	if err != nil || !ok {
		return nil, st, err
	}
	edit, err := codec{r}.edit(st.Workspace.Items)
	if err == nil {
		// As committing it does (codec.Build): the settings the base has
		// that the item does not are removed.
		settings, _ := st.Base.Get(settingsRef).(map[string]any)
		removeSettingsBut(slices.Collect(maps.Keys(settings)), &edit)
	}
	return &Draft{Base: strconv.FormatInt(st.Workspace.Base, 10), Edit: edit}, st, err
}

// PreviewInWorkspace previews a plan against the caller's workspace, as an
// agent working for them does.
func (r *Service) PreviewInWorkspace(ctx context.Context, p configplan.Plan, review bool) (*Preview, error) {
	if _, err := auth.Require(ctx, auth.PermWrite); err != nil {
		return nil, err
	}
	return r.PreviewPlan(ctx, p, Against{Workspace: true}, review)
}

// applicable checks that in.PlanID names a preview of the plan the caller
// made (kept, or the plan as it evaluates now), and that the plan, evaluated
// against pc, changes exactly what that preview showed (the same values to
// the same values; plans previewed together and changing different values,
// even of one item, apply one after another), can be taken and has each
// exception to confirm confirmed; it returns that evaluation.
func (r *Service) applicable(
	ctx context.Context, who auth.Principal, pc planContext, in ApplyRequest,
) (*Preview, error) {
	prev, ok := r.previews.get(in.PlanID, who.Username, r.now())
	if ok && (reaches(who, prev) != nil || prev.Review != in.Review || !samePlan(prev.Plan, in.Plan)) ||
		!ok && in.PlanID != planID(pc, in.Plan, in.Review) {
		return nil, ErrPlanChanged
	}
	p, err := r.evaluate(ctx, pc, in.Plan, in.Review)
	if err != nil {
		return nil, err
	}
	if ok && prev.effect != p.effect {
		return nil, ErrPlanChanged
	}
	if err := takeable(p); err != nil {
		return nil, err
	}
	if !ok {
		prev = p
	}
	return p, confirmed(p.Report, prev.Report, in.Confirmed)
}

func samePlan(a, b configplan.Plan) bool {
	x, errA := json.Marshal(a)
	y, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(x) == string(y)
}

// ApplyRequest applies a plan to the caller's workspace.
type ApplyRequest struct {
	Plan   configplan.Plan
	PlanID string
	Review bool
	// Confirmed are the IDs of the exceptions to confirm reported to the
	// administrator: every one of them.
	Confirmed []string
	Comment   string
	// Agent names the agent (its token) applying the plan; empty: the
	// administrator, in the console.
	Agent string
}

// AppliedPlan is what applying a plan wrote.
type AppliedPlan struct {
	Items   []WorkspaceRef `json:"items"`
	Comment string         `json:"comment,omitempty"`
}

// ApplyPlan writes a previewed plan (see applicable) into the caller's
// workspace: the items it changes, as the console writes them, with a note
// of the agent (if one applies it) and its comment. The console's "add to
// the workspace" and an agent's plan_apply both come here. A plan that
// changes nothing, does not validate or has blocking exceptions fails.
func (r *Service) ApplyPlan(ctx context.Context, in ApplyRequest) (*AppliedPlan, error) {
	// Applying recomputes the preview: it reads what previewing reads.
	if _, err := auth.Require(ctx, auth.PermRead); err != nil {
		return nil, err
	}
	who, err := auth.Require(ctx, auth.PermWrite)
	if err != nil {
		return nil, err
	}
	if err := normalized(who, &in.Plan); err != nil {
		return nil, err
	}
	draft, st, err := r.workspaceDraft(ctx, who.Username)
	if err != nil {
		return nil, err
	}
	if draft == nil {
		return nil, workspaceops.ErrNothingPublished
	}
	pc, err := r.planContext(ctx, draft)
	if err != nil {
		return nil, err
	}
	p, err := r.applicable(ctx, who, pc, in)
	if err != nil {
		return nil, err
	}
	writes := p.writes(st.Workspace.Items)
	by, comment := who.Username, strings.TrimSpace(in.Comment)
	var note *workspace.Note
	if in.Agent != "" {
		by, note = "agent:"+in.Agent, &workspace.Note{Source: "agent", By: in.Agent, Text: comment}
	}
	ops, err := r.workspaces()
	if err != nil {
		return nil, err
	}
	if _, err := ops.WriteItems(ctx, who.Username, by, "apply", writes, note); err != nil {
		return nil, err
	}
	out := &AppliedPlan{Items: make([]WorkspaceRef, 0, len(writes)), Comment: comment}
	for _, w := range writes {
		out.Items = append(out.Items, WorkspaceRef(w.Ref))
	}
	return out, nil
}

// changes are the items a plan's outcome (spec, canonical, as its payload
// says) changes from what it was
// evaluated against (pc), with their values after, and the effect:
// the values it changes (item, path, value after), canonical, to compare
// evaluations: a change made meanwhile to another value of the same item
// leaves it as it was.
func (r *Service) changes(pc planContext, spec *config.Spec) (workspace.Snapshot, string, error) {
	before, err := itemsOfSpec(pc.spec)
	if err != nil {
		return nil, "", err
	}
	after, err := itemsOfSpec(spec)
	if err != nil {
		return nil, "", err
	}
	var changed workspace.Snapshot
	var effect []string
	beforeAt, afterAt := before.Index(), after.Index()
	get := func(s workspace.Snapshot, at map[workspace.Ref]int, ref workspace.Ref) any {
		if i, ok := at[ref]; ok {
			return s[i].Value
		}
		return workspace.Missing
	}
	for _, ref := range Schema.Changes(before, after) {
		now := get(after, afterAt, ref)
		changed = append(changed, workspace.Item{Ref: ref, Value: now})
		for _, c := range Schema.Diff(get(before, beforeAt, ref), now) {
			effect = append(effect, ref.Kind+"\x00"+ref.Key+"\x00"+c.Path.String()+"\x00"+Schema.Canonical(c.Value))
		}
	}
	slices.Sort(effect)
	return changed, strings.Join(effect, "\n"), nil
}

// writes are the writes of p's changes into a workspace holding current.
func (p *Preview) writes(current workspace.Snapshot) []workspaceops.Write {
	out := make([]workspaceops.Write, 0, len(p.changed))
	at := current.Index()
	for _, it := range p.changed {
		previous := workspace.Missing
		if i, ok := at[it.Ref]; ok {
			previous = current[i].Value
		}
		out = append(out, workspaceops.Write{Ref: it.Ref, Value: it.Value, Previous: previous})
	}
	return out
}
