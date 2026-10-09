package graph

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

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
		Key: p.Key, Status: pendingStatus(p.Status, p.Until, now), Owner: optional(p.Owner),
		Reason: optional(p.Reason), UpdatedBy: p.UpdatedBy, UpdatedAt: p.UpdatedAt,
	}
	if !p.Until.IsZero() {
		out.Until = &p.Until
	}
	return out
}

func (r *Resolver) pendingStates(ctx context.Context) (map[string]*PendingState, error) {
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
		out[p.Key] = toPendingState(p, r.Scans.clock())
	}
	return out, nil
}

// pending evaluates every recurring template, reporting each that would
// change the configuration or has rules it cannot apply, lists the tables
// whose fields differ, and counts the tables not imported and the schemas
// never scanned.
func (r *Resolver) pending(ctx context.Context, in *DraftInput) (*PendingWork, error) {
	if _, err := auth.Require(ctx, auth.PermWrite); err != nil {
		return nil, err
	}
	pc, err := r.planContext(ctx, in)
	if err != nil {
		return nil, err
	}
	states, err := r.pendingStates(ctx)
	if err != nil {
		return nil, err
	}
	out := &PendingWork{Plans: []PendingPlan{}, DriftTables: []PendingTable{}, UnlistedDatasources: []string{}}
	rows, counts, _ := r.Scans.catalogRows(pc, CatalogFilter{})
	out.Drift, out.NewTables = counts.Drift, counts.New
	for _, row := range rows {
		if row.Drift() && row.Entity != nil {
			key := "drift:" + row.Ref + ":" + digest([][]string{row.Added, row.Missing})
			out.DriftTables = append(out.DriftTables, PendingTable{Key: key, Ref: row.Ref, Entity: row.Entity.ID(),
				Added: orEmpty(row.Added), Missing: orEmpty(row.Missing), State: states[key]})
		}
	}
	for _, schemas := range configplan.Unscanned(pc.sources, configplan.Scope{}) {
		out.UnscannedSchemas += len(schemas)
	}
	for _, src := range pc.sources {
		if src.Listed == nil && len(src.Scanned) == 0 {
			out.UnlistedDatasources = append(out.UnlistedDatasources, src.Datasource)
		}
	}
	out.Plans, err = r.pendingPlans(ctx, pc, states)
	return out, err
}

func (r *Resolver) pendingPlans(
	ctx context.Context, pc planContext, states map[string]*PendingState,
) ([]PendingPlan, error) {
	templates, err := r.planTemplates(ctx)
	if err != nil {
		return nil, err
	}
	out := []PendingPlan{}
	for _, t := range templates {
		if !t.Recurring {
			continue
		}
		p, err := decodePlan(t.Plan)
		if err != nil {
			return nil, err
		}
		proposal, err := configplan.Evaluate(r.planInput(pc, true), p)
		if err != nil {
			return nil, err
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

var pendingStatuses = []string{"open", "snoozed", "ignored"}

func (r *Resolver) setPendingState(ctx context.Context, in PendingStateInput) (*PendingState, error) {
	who, err := auth.Require(ctx, auth.PermWrite)
	if err != nil {
		return nil, err
	}
	store, err := r.plans()
	if err != nil {
		return nil, err
	}
	state := configstore.PendingState{
		Key: strings.TrimSpace(
			in.Key,
		), Status: in.Status, Owner: deref(in.Owner), Reason: strings.TrimSpace(deref(in.Reason)),
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
	case state.Status == "snoozed" && !state.Until.After(r.Scans.clock()):
		return nil, errors.New("a snooze needs a time in the future")
	case state.Status == "ignored" && state.Reason == "":
		return nil, errors.New("say why the item is ignored")
	}
	saved, err := store.PutPendingState(ctx, state)
	if err != nil {
		return nil, err
	}
	return toPendingState(saved, r.Scans.clock()), nil
}

func (r *Resolver) pendingLog(ctx context.Context, key string) ([]PendingState, error) {
	if _, err := auth.Require(ctx, auth.PermRead); err != nil {
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
