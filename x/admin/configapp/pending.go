package configapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/nethinwei/sql-mcp-server/internal/kit"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/configplan"
	"github.com/nethinwei/sql-mcp-server/x/configstore"
)

// Pending items are keyed by what they are about and a digest of the
// situation, so an item ignored or snoozed opens again when the situation
// changes (new matches, other differences).

func digest(v any) string {
	data, _ := json.Marshal(v)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:6])
}

func toPendingState(p configstore.PendingState, now time.Time) *PendingState {
	out := &PendingState{
		Key: p.Key, Status: pendingStatus(p.Status, p.Until, now), Owner: kit.Optional(p.Owner),
		Reason: kit.Optional(p.Reason), UpdatedBy: p.UpdatedBy, UpdatedAt: p.UpdatedAt,
	}
	if !p.Until.IsZero() {
		out.Until = &p.Until
	}
	return out
}

func (r *Service) pendingStates(ctx context.Context) (map[string]*PendingState, error) {
	store, err := r.plans()
	if err != nil {
		return nil, err
	}
	all, err := store.PendingStates(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*PendingState, len(all))
	for _, p := range all {
		out[p.Key] = toPendingState(p, r.now())
	}
	return out, nil
}

// Pending evaluates every recurring template, reporting each that would
// change the configuration or has rules it cannot apply, lists the tables
// whose fields differ, and counts the tables not imported and the schemas
// never scanned. A principal limited to some datasources sees what is
// about them only: their tables and the templates whose scope names only
// them.
func (r *Service) Pending(ctx context.Context, against Against) (*PendingWork, error) {
	who, err := auth.Require(ctx, auth.PermWrite)
	if err != nil {
		return nil, err
	}
	pc, err := r.planContextOf(ctx, against)
	if err != nil {
		return nil, err
	}
	states, err := r.pendingStates(ctx)
	if err != nil {
		return nil, err
	}
	out := &PendingWork{
		Plans: []PendingPlan{}, DriftTables: []PendingTable{}, GoneTables: []PendingGoneTable{},
		UnlistedDatasources: []string{},
	}
	var f CatalogFilter
	if err := withinFilter(who, &f); err != nil {
		return nil, err
	}
	positions, counts, x := r.Scans.catalogRows(pc, f)
	out.Drift, out.NewTables = counts.Drift, counts.New
	for _, at := range positions {
		row := x.At(at)
		if row.Drift() && row.Entity != nil {
			key := "drift:" + row.Ref + ":" + digest([][]string{row.Added, row.Missing})
			out.DriftTables = append(out.DriftTables, PendingTable{Key: key, Ref: row.Ref, Entity: row.Entity.ID(),
				Added: kit.OrEmpty(row.Added), Missing: kit.OrEmpty(row.Missing), State: states[key]})
		}
	}
	scope := configplan.Scope{Datasources: f.Datasources}
	out.DriftTables, out.MoreDriftTables = firstPending(out.DriftTables)
	out.GoneTables, out.MoreGoneTables = firstPending(pendingGone(pc, scope, who, states))
	for _, schemas := range configplan.Unscanned(pc.sources, scope) {
		out.UnscannedSchemas += len(schemas)
	}
	for _, src := range pc.sources {
		if who.Allows(src.Datasource) && src.Listed == nil && len(src.Scanned) == 0 {
			out.UnlistedDatasources = append(out.UnlistedDatasources, src.Datasource)
		}
	}
	out.Plans, err = r.pendingPlans(ctx, pc, states, who)
	return out, err
}

// pendingGone lists the configured tables and views of scope whose tables
// are gone.
func pendingGone(
	pc planContext, scope configplan.Scope, who auth.Principal, states map[string]*PendingState,
) []PendingGoneTable {
	out := []PendingGoneTable{}
	for _, g := range configplan.GoneTables(pc.sources, pc.base, scope, pc.missing.Tables) {
		if who.Allows(g.Datasource) {
			key := "gone:" + g.Entity
			out = append(out,
				PendingGoneTable{Key: key, Ref: g.Ref, Entity: g.Entity, Scanned: g.Scanned, State: states[key]})
		}
	}
	return out
}

// pendingTables bounds the drift and gone tables Pending lists: one
// migration may touch thousands, and the configuration MCP hands the whole
// answer to an agent; the counts cover them all.
const pendingTables = 200

// firstPending keeps the first pendingTables of items, those not handled
// (open) first, and counts the rest.
func firstPending[T interface{ pendingState() *PendingState }](items []T) ([]T, int) {
	if len(items) <= pendingTables {
		return items, 0
	}
	handled := func(x T) int {
		if s := x.pendingState(); s != nil && s.Status != "open" {
			return 1
		}
		return 0
	}
	slices.SortStableFunc(items, func(x, y T) int { return handled(x) - handled(y) })
	return items[:pendingTables], len(items) - pendingTables
}

func (t PendingTable) pendingState() *PendingState     { return t.State }
func (t PendingGoneTable) pendingState() *PendingState { return t.State }

func (r *Service) pendingPlans(
	ctx context.Context, pc planContext, states map[string]*PendingState, who auth.Principal,
) ([]PendingPlan, error) {
	templates, err := r.templates(ctx, who)
	if err != nil {
		return nil, err
	}
	out := []PendingPlan{}
	for _, t := range templates {
		if !t.Recurring {
			continue
		}
		proposal, err := r.evaluateTemplate(pc, t)
		if err != nil {
			// A template that no longer evaluates (its exclusions name a
			// table since dropped, ...) is reported, not the whole view.
			msg := err.Error()
			key := "template:" + t.Name + ":" + digest([]any{t.Version, msg})
			out = append(out, PendingPlan{Key: key, Template: &t, Report: toPlanReport(configplan.Report{}),
				Error: &msg, State: states[key]})
			continue
		}
		unapplied := slices.ContainsFunc(
			proposal.Report.Exceptions,
			func(x configplan.Exception) bool { return x.Warning },
		)
		if proposal.Empty() && !unapplied {
			continue
		}
		report := toPlanReport(proposal.Report)
		key := "template:" + t.Name + ":" + digest([]any{t.Version, report.Changes, report.Exceptions})
		out = append(out, PendingPlan{Key: key, Template: &t, Report: report, State: states[key]})
	}
	return out, nil
}

func (r *Service) evaluateTemplate(pc planContext, t PlanTemplate) (configplan.Proposal, error) {
	p, err := DecodePlan(t.Plan)
	if err != nil {
		return configplan.Proposal{}, err
	}
	return configplan.Evaluate(r.planInput(pc, true), p)
}

var pendingStatuses = []string{"open", "snoozed", "ignored"}

// SetPendingState records who handles a pending item and how.
func (r *Service) SetPendingState(ctx context.Context, in PendingStateInput) (*PendingState, error) {
	who, store, err := r.planStore(ctx, auth.PermWrite)
	if err != nil {
		return nil, err
	}
	state := configstore.PendingState{
		Key: strings.TrimSpace(
			in.Key,
		), Status: in.Status, Owner: kit.Deref(in.Owner), Reason: strings.TrimSpace(kit.Deref(in.Reason)),
		UpdatedBy: who.Username,
	}
	if in.Until != nil {
		state.Until = *in.Until
	}
	switch {
	case state.Key == "" || len(state.Key) > 255:
		return nil, errors.New("a pending item key has 1 to 255 bytes")
	case !slices.Contains(pendingStatuses, state.Status):
		return nil, errors.New("status is open, snoozed or ignored")
	case state.Status == "snoozed" && !state.Until.After(r.now()):
		return nil, errors.New("a snooze needs a time in the future")
	case state.Status == "ignored" && state.Reason == "":
		return nil, errors.New("say why the item is ignored")
	}
	saved, err := store.PutPendingState(ctx, state)
	if err != nil {
		return nil, err
	}
	return toPendingState(saved, r.now()), nil
}

// PendingLog lists what was set of a pending item, newest first.
func (r *Service) PendingLog(ctx context.Context, key string) ([]PendingState, error) {
	if _, err := auth.RequireUnlimited(ctx, auth.PermRead); err != nil {
		return nil, err
	}
	store, err := r.plans()
	if err != nil {
		return nil, err
	}
	all, err := store.PendingLog(ctx, key)
	if err != nil {
		return nil, err
	}
	out := make([]PendingState, 0, len(all))
	for _, p := range all {
		s := toPendingState(p, time.Time{})
		s.Status = p.Status // the log shows what was set
		out = append(out, *s)
	}
	return out, nil
}
