package graph

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/configplan"
	"github.com/nethinwei/sql-mcp-server/x/revisionops"
)

// errPlanChanged: what a plan evaluates to changed since its preview.
var errPlanChanged = errors.New("the plan's outcome changed since it was previewed (a publish, a draft edit or a " +
	"new scan): preview it again")

func toPlanReport(r configplan.Report) *PlanReport {
	out := &PlanReport{
		Matched: r.Matched, Excluded: r.Excluded, Objects: make([]PlanObjects, 0, len(r.Objects)),
		Changes: make([]PlanChange, 0, len(r.Changes)), Exceptions: make([]PlanException, 0, len(r.Exceptions)),
		Unscanned: []UnscannedSchemas{},
	}
	if !r.ScannedFrom.IsZero() {
		out.ScannedFrom, out.ScannedTo = &r.ScannedFrom, &r.ScannedTo
	}
	for _, o := range r.Objects {
		out.Objects = append(out.Objects, PlanObjects{
			Datasource: o.Datasource, Schema: o.Schema, Tables: o.Tables, Imported: o.Imported, Changed: o.Changed,
		})
	}
	for _, c := range r.Changes {
		out.Changes = append(out.Changes, PlanChange{
			Kind: c.Kind, Principal: optional(c.Principal), Actions: orEmpty(c.Actions), Mask: optional(c.Mask),
			Fields: c.Fields, Entities: orEmpty(c.Entities),
		})
	}
	for _, x := range r.Exceptions {
		out.Exceptions = append(out.Exceptions, PlanException{
			Kind: x.Kind, Level: x.Level, Warning: x.Warning, Ref: optional(x.Ref), Entity: optional(x.Entity),
			Detail: optional(x.Detail),
		})
	}
	datasources := make([]string, 0, len(r.Unscanned))
	for ds := range r.Unscanned {
		datasources = append(datasources, ds)
	}
	sort.Strings(datasources)
	for _, ds := range datasources {
		out.Unscanned = append(out.Unscanned, UnscannedSchemas{Datasource: ds, Schemas: r.Unscanned[ds]})
	}
	return out
}

// createPlanDraft saves base (default: the published revision) with a plan
// applied as a draft. With wantID, the plan must still evaluate to what was
// previewed under that ID. A plan that changes nothing, whose outcome does
// not validate or that has blocking exceptions fails.
func (r *Resolver) createPlanDraft(ctx context.Context, plan any, comment, base, wantID *string) (*Revision, error) {
	who, err := auth.Require(ctx, auth.PermWrite)
	if err != nil {
		return nil, err
	}
	p, err := decodePlan(plan)
	if err != nil {
		return nil, err
	}
	var in *DraftInput
	if base != nil {
		in = &DraftInput{Base: *base}
	}
	pc, err := r.planContext(ctx, in)
	if err != nil {
		return nil, err
	}
	if wantID != nil && *wantID != planID(pc, p) {
		return nil, apiError(errPlanChanged)
	}
	proposal, d, validation, err := r.evaluatePlan(ctx, pc, p, false)
	if err != nil {
		return nil, err
	}
	if err := takeable(proposal, validation); err != nil {
		return nil, err
	}
	text := deref(comment)
	if text == "" {
		text = "plan"
	}
	rev, _, err := r.ops().Draft(ctx, revisionops.DraftRequest{
		Payload: d.payload, Parent: pc.baseID, Author: who.Username, Comment: text,
	})
	if err != nil {
		return nil, err
	}
	return toRevision(rev), nil
}

// takeable reports why a proposal cannot be saved.
func takeable(p configplan.Proposal, v *Validation) error {
	if p.Empty() {
		return errors.New("the plan changes nothing")
	}
	if !v.Ok {
		return fmt.Errorf("the plan's outcome does not validate: %s", deref(v.Error))
	}
	for _, x := range p.Report.Exceptions {
		if x.Level == configplan.LevelBlock {
			return fmt.Errorf("the plan has a blocking exception (%s %s %s): exclude the table or fix it first",
				x.Kind, x.Entity, x.Detail)
		}
	}
	return nil
}

func (r *Resolver) plans() (PlanStore, error) {
	if r.Plans == nil {
		return nil, errors.New("plan templates and MCP tokens need a configuration store")
	}
	return r.Plans, nil
}

// pendingStatus is a state as it reads now: a snooze that ended is open.
func pendingStatus(status string, until time.Time, now time.Time) string {
	if status == "snoozed" && !until.IsZero() && !until.After(now) {
		return "open"
	}
	return status
}
