package configmcp

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/admin/configapp"
	"github.com/nethinwei/sql-mcp-server/x/configplan"
)

// Plan tools answer in layers: plan_preview a summary that fits an agent's
// context, plan_details what that preview holds, a page at a time, from the
// preview itself (it does not evaluate the plan again).

type planIn struct {
	Plan configplan.Plan `json:"plan"           jsonschema:"the plan: scope plus import, grants, fields or sync"`
	Base string          `json:"base,omitempty" jsonschema:"a draft revision ID to continue (default: published)"`
	// Review is set to review a recurring template's proposal.
	Review bool `json:"review,omitempty" jsonschema:"report every new table a grant covers, to confirm"`
}

type detailsIn struct {
	PlanID  string `json:"planId"           jsonschema:"the planId plan_preview returned"`
	Section string `json:"section"          jsonschema:"changes, changeEntities, exceptions, added, entities or objects"`
	Change  int    `json:"change,omitempty" jsonschema:"for changeEntities: the index of the change, as changes lists it"`
	Kind    string `json:"kind,omitempty"   jsonschema:"only changes or exceptions of this kind"`
	Level   string `json:"level,omitempty"  jsonschema:"only exceptions of this level: block, confirm or info"`
	Offset  int    `json:"offset,omitempty"`
	Limit   int    `json:"limit,omitempty"  jsonschema:"items to return, at most 200 (default 50)"`
}

type saveIn struct {
	planIn
	PlanID string `json:"planId"              jsonschema:"the planId plan_preview returned for this plan and base"`
	// Confirmed binds the administrator's confirmations to exceptions.
	Confirmed []string `json:"confirmed,omitempty" jsonschema:"IDs of the exceptions the administrator confirmed"`
	Comment   string   `json:"comment,omitempty"   jsonschema:"why, for the reviewer"`
}

type changeSummary struct {
	Index     int      `json:"index"`
	Kind      string   `json:"kind"`
	Principal string   `json:"principal,omitempty"`
	Actions   []string `json:"actions,omitempty"`
	Mask      string   `json:"mask,omitempty"`
	Fields    int      `json:"fields,omitempty"`
	Entities  int      `json:"entities"`
}

type exceptionSummary struct {
	Kind  string `json:"kind"`
	Level string `json:"level"`
	Count int    `json:"count"`
}

// confirmListed bounds the IDs of exceptions to confirm a summary lists.
const confirmListed = 100

type previewSummary struct {
	PlanID     string             `json:"planId"`
	Matched    int                `json:"matched"`
	Excluded   int                `json:"excluded"`
	Added      int                `json:"added"`
	Changed    int                `json:"changed"`
	Changes    []changeSummary    `json:"changes"`
	Exceptions []exceptionSummary `json:"exceptions"`
	Blocking   int                `json:"blocking"`
	ToConfirm  int                `json:"toConfirm"`
	// ToConfirmIDs lists the first exceptions to confirm; page through all
	// of them with plan_details (section exceptions, level confirm).
	ToConfirmIDs []string              `json:"toConfirmIds,omitempty"`
	Unscanned    int                   `json:"unscannedSchemas"`
	ScannedFrom  any                   `json:"scannedFrom,omitempty"`
	Validation   *configapp.Validation `json:"validation"`
}

func (in planIn) draft() *configapp.Draft {
	if in.Base == "" {
		return nil
	}
	return &configapp.Draft{Base: in.Base}
}

func summarize(p *configapp.Preview) previewSummary {
	r := p.Report
	out := previewSummary{
		PlanID: p.PlanID, Matched: r.Matched, Excluded: r.Excluded, Added: len(p.Proposal.Added),
		Changed:    len(p.Proposal.Entities) - len(p.Proposal.Added) + len(p.Proposal.Roles) + len(p.Proposal.Users),
		Validation: p.Validation, Changes: changes(r, ""), Exceptions: []exceptionSummary{},
	}
	if r.ScannedFrom != nil {
		out.ScannedFrom = r.ScannedFrom
	}
	for _, x := range r.Exceptions {
		switch x.Level {
		case configplan.LevelBlock:
			out.Blocking++
		case configplan.LevelConfirm:
			out.ToConfirm++
			if len(out.ToConfirmIDs) < confirmListed {
				out.ToConfirmIDs = append(out.ToConfirmIDs, x.ID)
			}
		}
		i := slices.IndexFunc(out.Exceptions, func(e exceptionSummary) bool { return e.Kind == x.Kind && e.Level == x.Level })
		if i < 0 {
			out.Exceptions = append(out.Exceptions, exceptionSummary{Kind: x.Kind, Level: x.Level})
			i = len(out.Exceptions) - 1
		}
		out.Exceptions[i].Count++
	}
	for _, u := range r.Unscanned {
		out.Unscanned += len(u.Schemas)
	}
	return out
}

// changes summarizes the changes of a report (of a kind), with how many
// entities each covers.
func changes(r *configapp.PlanReport, kind string) []changeSummary {
	out := []changeSummary{}
	for i, c := range r.Changes {
		if kind == "" || c.Kind == kind {
			out = append(out, changeSummary{Index: i, Kind: c.Kind, Principal: deref(c.Principal), Actions: c.Actions,
				Mask: deref(c.Mask), Fields: c.Fields, Entities: len(c.Entities)})
		}
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// page returns items[offset:offset+limit] with the total.
func page[T any](items []T, offset, limit int) map[string]any {
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	offset = min(max(offset, 0), len(items))
	return map[string]any{"total": len(items), "offset": offset, "items": items[offset:min(offset+limit, len(items))]}
}

func details(p *configapp.Preview, in detailsIn) (any, error) {
	r := p.Report
	switch in.Section {
	case "changes":
		return page(changes(r, in.Kind), in.Offset, in.Limit), nil
	case "changeEntities":
		if in.Change < 0 || in.Change >= len(r.Changes) {
			return nil, errors.New("change is the index of a change, as the changes section lists it")
		}
		return page(r.Changes[in.Change].Entities, in.Offset, in.Limit), nil
	case "exceptions":
		items := slices.DeleteFunc(slices.Clone(r.Exceptions), func(x configapp.PlanException) bool {
			return in.Kind != "" && x.Kind != in.Kind || in.Level != "" && x.Level != in.Level
		})
		return page(items, in.Offset, in.Limit), nil
	case "added":
		return page(p.Proposal.Added, in.Offset, in.Limit), nil
	case "entities":
		return page(p.Proposal.Entities, in.Offset, in.Limit), nil
	case "objects":
		return page(r.Objects, in.Offset, in.Limit), nil
	}
	return nil, errors.New("section is changes, changeEntities, exceptions, added, entities or objects")
}

func registerPlans(s *mcp.Server, app *configapp.Service) {
	add(s, tool("plan_preview", "Evaluate a plan without saving: a summary of what it covers and changes, its "+
		"exceptions by kind and level (with the IDs of those to confirm), whether the outcome validates, and the "+
		"planId plan_details and plan_save_draft take.", true),
		func(ctx context.Context, in planIn) (any, error) {
			p, err := app.PreviewPlan(ctx, in.Plan, in.draft(), in.Review)
			if err != nil {
				return nil, err
			}
			return summarize(p), nil
		})
	add(s, tool("plan_details", "One section of a preview, a page at a time, read from what plan_preview "+
		"evaluated (by its planId): changes (with how many entities each covers), changeEntities (the entity IDs "+
		"of one change, by index), exceptions (with their IDs), added (entity IDs), entities (whole, as changed) or "+
		"objects (counts by schema).", true),
		func(ctx context.Context, in detailsIn) (any, error) {
			p, err := app.Previewed(ctx, in.PlanID)
			if err != nil {
				return nil, err
			}
			return details(p, in)
		})
	add(s, tool("plan_save_draft", "Save the plan's outcome as a draft revision, applied to the published "+
		"revision or to base (a draft, to continue it), only when it still evaluates to planId and every "+
		"exception to confirm is confirmed (report them to the administrator first). Returns the draft, the size "+
		"of its diff (read it with diff) and where an administrator reviews and publishes it; it never publishes.",
		false),
		saveDraft(app))
}

// saveDraft saves a plan's outcome as a draft and says where it is reviewed.
func saveDraft(app *configapp.Service) func(context.Context, saveIn) (any, error) {
	return func(ctx context.Context, in saveIn) (any, error) {
		if in.PlanID == "" {
			return nil, errors.New("pass the planId plan_preview returned")
		}
		req := configapp.SaveRequest{Plan: in.Plan, PlanID: in.PlanID, Review: in.Review, Confirmed: in.Confirmed,
			Comment: in.Comment}
		if in.Base != "" {
			req.Base = &in.Base
		}
		rev, err := app.SaveDraft(ctx, req)
		if err != nil {
			return nil, err
		}
		out := toRevisionOut(rev)
		if out.Parent != "" {
			// A token limited to some datasources does not read the whole
			// configuration's diff.
			diff, err := app.Diff(ctx, out.Parent, &out.ID, nil)
			if err != nil && !errors.Is(err, auth.ErrOutOfScope) {
				return nil, err
			}
			out.DiffLines = strings.Count(diff, "\n")
		}
		out.ReviewURL = reviewURL(ctx, app, out.ID)
		return out, nil
	}
}
