package configapp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/configplan"
	"github.com/nethinwei/sql-mcp-server/x/revisionops"
)

// A preview is kept, by its plan ID, for whoever made it: the objects and
// exceptions of a large plan are read a page at a time from what was
// previewed, not from a new evaluation, and a draft is saved from it only
// while the plan still evaluates to it (same base, draft and scans), with
// each exception to confirm confirmed by its ID.

const (
	previewsKept = 32
	previewTTL   = 15 * time.Minute
)

// Preview is a plan evaluated against a configuration.
type Preview struct {
	// PlanID identifies the plan with what it was evaluated against.
	PlanID   string
	Plan     configplan.Plan
	Review   bool
	Proposal configplan.Proposal
	// Base is the configuration the plan applies to.
	Base       *config.Config
	Report     *PlanReport
	Validation *Validation
	// payload is the outcome, normalized; nil when it does not validate.
	payload []byte
	baseID  int64
	owner   string
	at      time.Time
}

// previews keeps recent previews.
type previews struct {
	mu  sync.Mutex
	all []*Preview // most recently used last
}

func (k *previews) put(p *Preview) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.all = slices.DeleteFunc(k.all, func(o *Preview) bool { return o.PlanID == p.PlanID && o.owner == p.owner })
	k.all = append(k.all, p)
	if len(k.all) > previewsKept {
		k.all = k.all[len(k.all)-previewsKept:]
	}
}

func (k *previews) get(id, owner string, now time.Time) (*Preview, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	for i, p := range k.all {
		if p.PlanID == id && p.owner == owner && now.Sub(p.at) < previewTTL {
			k.all = append(slices.Delete(k.all, i, i+1), p)
			return p, true
		}
	}
	return nil, false
}

// PreviewPlan evaluates a plan against draft (default: the published
// configuration), as a review of a recurring template's proposal when
// review is set, and keeps the result for its maker.
func (r *Service) PreviewPlan(ctx context.Context, p configplan.Plan, draft *Draft, review bool) (*Preview, error) {
	who, err := auth.Require(ctx, auth.PermWrite)
	if err != nil {
		return nil, err
	}
	if err := normalized(who, &p); err != nil {
		return nil, err
	}
	pc, err := r.planContext(ctx, draft)
	if err != nil {
		return nil, err
	}
	out, err := r.evaluate(ctx, pc, p, review)
	if err != nil {
		return nil, err
	}
	out.owner, out.at = who.Username, r.now()
	r.previews.put(out)
	return out, nil
}

// Previewed returns a preview its caller made, while it is kept.
func (r *Service) Previewed(ctx context.Context, planID string) (*Preview, error) {
	who, err := auth.Require(ctx, auth.PermWrite)
	if err != nil {
		return nil, err
	}
	p, ok := r.previews.get(planID, who.Username, r.now())
	if !ok {
		return nil, fmt.Errorf("no preview %q is kept: preview the plan again", planID)
	}
	if err := reaches(who, p); err != nil {
		return nil, err
	}
	return p, nil
}

// reaches checks that who may read what a preview holds: a preview is kept
// for its maker's account, and one of its tokens may be limited to fewer
// datasources than the token (or session) that made it. A limited principal
// reads only a preview whose scope names datasources it reaches, all of
// them.
func reaches(who auth.Principal, p *Preview) error {
	if !who.Limited() {
		return nil
	}
	if len(p.Plan.Scope.Datasources) == 0 {
		return fmt.Errorf("%w: the preview spans every datasource", auth.ErrOutOfScope)
	}
	for _, ds := range p.Plan.Scope.Datasources {
		if !who.Allows(ds) {
			return fmt.Errorf("%w: the preview reaches %q", auth.ErrOutOfScope, ds)
		}
	}
	return nil
}

// normalized checks a plan and puts it in its normal form (actions lower
// case, sorted and once each), within who's datasources: a preview and a save
// of the same plan, however it was written, get the same plan ID.
func normalized(who auth.Principal, p *configplan.Plan) error {
	p.Grants = slices.Clone(p.Grants) // Check rewrites them; the caller's stay as they are
	for i := range p.Grants {
		p.Grants[i].Actions = slices.Clone(p.Grants[i].Actions)
	}
	if err := p.Check(); err != nil {
		return err
	}
	return within(who, &p.Scope)
}

// SaveRequest saves a plan's outcome as a draft.
type SaveRequest struct {
	Plan configplan.Plan
	// Base is a draft to continue; default: the published revision.
	Base *string
	// PlanID, when set, is the preview the outcome must still be.
	PlanID string
	Review bool
	// Confirmed are the IDs of the exceptions to confirm the caller
	// confirmed; with PlanID, it must name every one of them.
	Confirmed []string
	Comment   string
}

// SaveDraft saves base (default: the published revision) with a plan
// applied as a draft. With PlanID, the plan must still evaluate to what was
// previewed under that ID, and every exception to confirm must be
// confirmed. A plan that changes nothing, whose outcome does not validate or
// that has blocking exceptions fails.
func (r *Service) SaveDraft(ctx context.Context, in SaveRequest) (revision.Revision, error) {
	who, err := auth.Require(ctx, auth.PermWrite)
	if err != nil {
		return revision.Revision{}, err
	}
	if err := normalized(who, &in.Plan); err != nil {
		return revision.Revision{}, err
	}
	var draft *Draft
	if in.Base != nil {
		draft = &Draft{Base: *in.Base}
	}
	pc, err := r.planContext(ctx, draft)
	if err != nil {
		return revision.Revision{}, err
	}
	id := planID(pc, in.Plan, in.Review)
	if in.PlanID != "" && in.PlanID != id {
		return revision.Revision{}, ErrPlanChanged
	}
	p, ok := r.previews.get(id, who.Username, r.now())
	if ok && reaches(who, p) != nil {
		ok = false
	}
	if !ok {
		if p, err = r.evaluate(ctx, pc, in.Plan, in.Review); err != nil {
			return revision.Revision{}, err
		}
	}
	if err := takeable(p); err != nil {
		return revision.Revision{}, err
	}
	confirmed, err := confirmations(p.Report, in.Confirmed, in.PlanID != "")
	if err != nil {
		return revision.Revision{}, err
	}
	comment := strings.TrimSpace(in.Comment)
	if comment == "" {
		comment = "plan"
	}
	if len(confirmed) > 0 {
		comment += "\nconfirmed: " + strings.Join(confirmed, "; ")
	}
	rev, _, err := r.Ops().Draft(ctx, revisionops.DraftRequest{
		Payload: p.payload, Parent: p.baseID, Author: who.Username, Comment: comment,
	})
	return rev, err
}

// confirmations checks that ids confirm every exception to confirm of
// report (when required) and names nothing else, and describes them.
func confirmations(report *PlanReport, ids []string, required bool) ([]string, error) {
	pending := map[string]PlanException{}
	for _, x := range report.Exceptions {
		if x.Level == configplan.LevelConfirm {
			pending[x.ID] = x
		}
	}
	var out []string
	for _, id := range ids {
		x, ok := pending[id]
		if !ok {
			return nil, fmt.Errorf("%q is not an exception to confirm of this plan", id)
		}
		delete(pending, id)
		out = append(out, strings.TrimSpace(fmt.Sprintf("%s %s %s", x.Kind, deref(x.Entity), deref(x.Detail))))
	}
	if required && len(pending) > 0 {
		missing := make([]string, 0, len(pending))
		for id := range pending {
			missing = append(missing, id)
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("%d exceptions to confirm are not confirmed (%s): report them to the administrator "+
			"and pass their IDs as confirmed", len(missing), strings.Join(missing, ", "))
	}
	return out, nil
}

// takeable reports why a preview cannot be saved.
func takeable(p *Preview) error {
	if p.Proposal.Empty() {
		return errors.New("the plan changes nothing")
	}
	if !p.Validation.Ok {
		return fmt.Errorf("the plan's outcome does not validate: %s", deref(p.Validation.Error))
	}
	for _, x := range p.Proposal.Report.Exceptions {
		if x.Level == configplan.LevelBlock {
			return fmt.Errorf("the plan has a blocking exception (%s %s %s): exclude the table or fix it first",
				x.Kind, x.Entity, x.Detail)
		}
	}
	return nil
}

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
			ID: digest([]string{x.Kind, x.Ref, x.Entity, x.Detail}), Kind: x.Kind, Level: x.Level, Warning: x.Warning,
			Ref: optional(x.Ref), Entity: optional(x.Entity), Detail: optional(x.Detail),
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

func (r *Service) plans() (PlanStore, error) {
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
