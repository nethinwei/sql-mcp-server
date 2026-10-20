package configapp

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/workspace"
	"github.com/nethinwei/sql-mcp-server/internal/kit"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/configplan"
)

// A preview is kept, by its plan ID, for whoever made it: the objects and
// exceptions of a large plan are read a page at a time from what was
// previewed, not from a new evaluation, and an agent applies a plan only
// while it still evaluates to it (same workspace and scans), with each
// exception to confirm confirmed by its ID.

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
	// changed are the items the plan changes, with their values after;
	// effect is them canonical, what applying the preview binds to.
	changed workspace.Snapshot
	effect  string
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
	// Expired ones go too: a large import's preview holds a whole proposal.
	k.all = slices.DeleteFunc(k.all, func(o *Preview) bool {
		return o.PlanID == p.PlanID && o.owner == p.owner || p.at.Sub(o.at) >= previewTTL
	})
	k.all = append(k.all, p)
	if len(k.all) > previewsKept {
		k.all = k.all[len(k.all)-previewsKept:]
	}
}

func (k *previews) get(id, owner string, now time.Time) (*Preview, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.all = slices.DeleteFunc(k.all, func(p *Preview) bool { return now.Sub(p.at) >= previewTTL })
	for i, p := range k.all {
		if p.PlanID == id && p.owner == owner {
			k.all = append(slices.Delete(k.all, i, i+1), p)
			return p, true
		}
	}
	return nil, false
}

// PreviewPlan evaluates a plan against the configuration against is, as a
// review of a recurring template's proposal when review is set, and keeps
// the result for its maker.
func (r *Service) PreviewPlan(ctx context.Context, p configplan.Plan, against Against, review bool) (*Preview, error) {
	who, err := auth.Require(ctx, auth.PermWrite)
	if err != nil {
		return nil, err
	}
	if err := normalized(who, &p); err != nil {
		return nil, err
	}
	pc, err := r.planContextOf(ctx, against)
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
// case, sorted and once each; so are the sets of the scope and the tables
// excluded), within who's datasources: a preview and a save of the same
// plan, however it was written, get the same plan ID.
func normalized(who auth.Principal, p *configplan.Plan) error {
	p.Grants = slices.Clone(p.Grants) // Check rewrites them; the caller's stay as they are
	for i := range p.Grants {
		p.Grants[i].Actions = slices.Clone(p.Grants[i].Actions)
	}
	if err := p.Check(); err != nil {
		return err
	}
	if err := within(who, &p.Scope); err != nil {
		return err
	}
	sc := &p.Scope
	sc.Datasources, sc.Schemas, sc.Tables = setOf(sc.Datasources), setOf(sc.Schemas), setOf(sc.Tables)
	sc.Status, sc.Refs, sc.Entities, p.Exclude = setOf(sc.Status), setOf(sc.Refs), setOf(sc.Entities), setOf(p.Exclude)
	return nil
}

// setOf is a copy of s sorted, each value once.
func setOf[T cmp.Ordered](s []T) []T {
	out := slices.Clone(s)
	slices.Sort(out)
	return slices.Compact(out)
}

// confirmed checks that ids cover the exceptions to confirm of report and
// name nothing else: an agent reports each to the administrator and passes
// it. An ID may also name one of the plan's preview (previewed), which the
// workspace may since have settled.
func confirmed(report, previewed *PlanReport, ids []string) error {
	pending, known := map[string]bool{}, map[string]bool{}
	for _, r := range []*PlanReport{report, previewed} {
		for _, x := range r.Exceptions {
			if x.Level == configplan.LevelConfirm {
				pending[x.ID] = pending[x.ID] || r == report
				known[x.ID] = true
			}
		}
	}
	for _, id := range ids {
		if !known[id] {
			return fmt.Errorf("%q is not an exception to confirm of this plan", id)
		}
		delete(pending, id)
	}
	for id, now := range pending {
		if !now {
			delete(pending, id)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	missing := make([]string, 0, len(pending))
	for id := range pending {
		missing = append(missing, id)
	}
	sort.Strings(missing)
	return fmt.Errorf("%d exceptions to confirm are not confirmed (%s): report them to the administrator "+
		"and pass their IDs as confirmed", len(missing), strings.Join(missing, ", "))
}

// takeable reports why a preview cannot be taken.
func takeable(p *Preview) error {
	if p.Proposal.Empty() {
		return errors.New("the plan changes nothing")
	}
	if !p.Validation.Ok {
		return fmt.Errorf("the plan's outcome does not validate: %s", kit.Deref(p.Validation.Error))
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
			Kind: c.Kind, Principal: kit.Optional(c.Principal), Actions: kit.OrEmpty(c.Actions), Mask: kit.Optional(c.Mask),
			Fields: c.Fields, Entities: kit.OrEmpty(c.Entities),
		})
	}
	for _, x := range r.Exceptions {
		out.Exceptions = append(out.Exceptions, PlanException{
			ID: digest([]string{x.Kind, x.Ref, x.Entity, x.Detail}), Kind: x.Kind, Level: x.Level, Warning: x.Warning,
			Ref: kit.Optional(x.Ref), Entity: kit.Optional(x.Entity), Detail: kit.Optional(x.Detail),
			ReasonDetail: ReasonOf(x.Reason),
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

// planStore is the plan store, for a caller holding perm who is not limited
// to some datasources (what the store keeps spans every one).
func (r *Service) planStore(ctx context.Context, perm string) (auth.Principal, PlanStore, error) {
	who, err := auth.RequireUnlimited(ctx, perm)
	if err != nil {
		return auth.Principal{}, nil, err
	}
	store, err := r.plans()
	return who, store, err
}

// pendingStatus is a state as it reads now: a snooze that ended is open.
func pendingStatus(status string, until time.Time, now time.Time) string {
	if status == "snoozed" && !until.IsZero() && !until.After(now) {
		return "open"
	}
	return status
}
