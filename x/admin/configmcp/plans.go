package configmcp

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nethinwei/sql-mcp-server/x/admin/graph"
	"github.com/nethinwei/sql-mcp-server/x/configplan"
)

// Plan tools answer in layers: plan_preview a summary that fits an agent's
// context, plan_details the objects and exceptions a page at a time.

type planIn struct {
	Plan configplan.Plan `json:"plan"           jsonschema:"the plan: scope plus import, grants, fields or sync"`
	Base string          `json:"base,omitempty" jsonschema:"a draft revision ID to continue (default: published)"`
	// Review is set to review a recurring template's proposal.
	Review bool `json:"review,omitempty" jsonschema:"report every new table a grant covers, to confirm"`
}

type detailsIn struct {
	planIn
	Section string `json:"section"          jsonschema:"changes, changeEntities, exceptions, added, entities or objects"`
	Change  int    `json:"change,omitempty" jsonschema:"for changeEntities: the index of the change, as changes lists it"`
	Kind    string `json:"kind,omitempty"   jsonschema:"only changes or exceptions of this kind"`
	Offset  int    `json:"offset,omitempty"`
	Limit   int    `json:"limit,omitempty"  jsonschema:"items to return, at most 200 (default 50)"`
}

type saveIn struct {
	planIn
	PlanID  string `json:"planId"            jsonschema:"the planId plan_preview returned for this plan and base"`
	Comment string `json:"comment,omitempty" jsonschema:"why, for the reviewer"`
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

type previewSummary struct {
	PlanID      string             `json:"planId"`
	Matched     int                `json:"matched"`
	Excluded    int                `json:"excluded"`
	Added       int                `json:"added"`
	Changed     int                `json:"changed"`
	Changes     []changeSummary    `json:"changes"`
	Exceptions  []exceptionSummary `json:"exceptions"`
	Blocking    int                `json:"blocking"`
	ToConfirm   int                `json:"toConfirm"`
	Unscanned   int                `json:"unscannedSchemas"`
	ScannedFrom any                `json:"scannedFrom,omitempty"`
	Validation  *graph.Validation  `json:"validation"`
}

func (in planIn) preview(ctx context.Context, r *graph.Resolver) (*graph.PlanPreview, error) {
	plan, err := planJSON(in.Plan)
	if err != nil {
		return nil, err
	}
	var draft *graph.DraftInput
	if in.Base != "" {
		draft = &graph.DraftInput{Base: in.Base}
	}
	return r.Query().PreviewPlan(ctx, graph.PlanPreviewInput{Plan: plan, Draft: draft, Review: &in.Review})
}

func summarize(p *graph.PlanPreview) previewSummary {
	out := previewSummary{
		PlanID: p.PlanID, Matched: p.Report.Matched, Excluded: p.Report.Excluded, Added: len(p.Added),
		Changed: len(p.Entities) - len(p.Added) + len(p.Roles) + len(p.Users), Validation: p.Validation,
		Changes: []changeSummary{}, Exceptions: []exceptionSummary{},
	}
	if p.Report.ScannedFrom != nil {
		out.ScannedFrom = p.Report.ScannedFrom
	}
	for i, c := range p.Report.Changes {
		out.Changes = append(
			out.Changes,
			changeSummary{Index: i, Kind: c.Kind, Principal: deref(c.Principal), Actions: c.Actions,
				Mask: deref(c.Mask), Fields: c.Fields, Entities: len(c.Entities)},
		)
	}
	for _, x := range p.Report.Exceptions {
		switch x.Level {
		case "block":
			out.Blocking++
		case "confirm":
			out.ToConfirm++
		}
		i := slices.IndexFunc(
			out.Exceptions,
			func(e exceptionSummary) bool { return e.Kind == x.Kind && e.Level == x.Level },
		)
		if i < 0 {
			out.Exceptions = append(out.Exceptions, exceptionSummary{Kind: x.Kind, Level: x.Level})
			i = len(out.Exceptions) - 1
		}
		out.Exceptions[i].Count++
	}
	for _, u := range p.Report.Unscanned {
		out.Unscanned += len(u.Schemas)
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

func details(p *graph.PlanPreview, in detailsIn) (any, error) {
	switch in.Section {
	case "changes":
		// Changes list how many entities each covers; changeEntities pages
		// through them.
		items := []changeSummary{}
		for i, c := range p.Report.Changes {
			if in.Kind == "" || c.Kind == in.Kind {
				items = append(
					items,
					changeSummary{Index: i, Kind: c.Kind, Principal: deref(c.Principal), Actions: c.Actions,
						Mask: deref(c.Mask), Fields: c.Fields, Entities: len(c.Entities)},
				)
			}
		}
		return page(items, in.Offset, in.Limit), nil
	case "changeEntities":
		if in.Change < 0 || in.Change >= len(p.Report.Changes) {
			return nil, errors.New("change is the index of a change, as the changes section lists it")
		}
		return page(p.Report.Changes[in.Change].Entities, in.Offset, in.Limit), nil
	case "exceptions":
		items := slices.DeleteFunc(slices.Clone(p.Report.Exceptions), func(x graph.PlanException) bool {
			return in.Kind != "" && x.Kind != in.Kind
		})
		return page(items, in.Offset, in.Limit), nil
	case "added":
		return page(p.Added, in.Offset, in.Limit), nil
	case "entities":
		return page(p.Entities, in.Offset, in.Limit), nil
	case "objects":
		return page(p.Report.Objects, in.Offset, in.Limit), nil
	}
	return nil, errors.New("section is changes, changeEntities, exceptions, added, entities or objects")
}

func registerPlans(s *mcp.Server, r *graph.Resolver) {
	add(s, tool("plan_preview", "Evaluate a plan without saving: a summary of what it covers and changes, its "+
		"exceptions by kind and level, whether the outcome validates, and the planId plan_save_draft takes. "+
		"Page through the details with plan_details.", true),
		func(ctx context.Context, in planIn) (any, error) {
			p, err := in.preview(ctx, r)
			if err != nil {
				return nil, err
			}
			return summarize(p), nil
		})
	add(s, tool("plan_details", "One section of a plan's preview, a page at a time: changes (with how many "+
		"entities each covers), changeEntities (the entity IDs of one change, by index), exceptions, added (entity "+
		"IDs), entities (whole, as changed) or objects (counts by schema).", true),
		func(ctx context.Context, in detailsIn) (any, error) {
			p, err := in.preview(ctx, r)
			if err != nil {
				return nil, err
			}
			return details(p, in)
		})
	add(s, tool("plan_save_draft", "Save the plan's outcome as a draft revision, applied to the published "+
		"revision or to base (a draft, to continue it), only when it still evaluates to planId. Returns the "+
		"draft, the size of its diff (read it with diff) and where an administrator reviews and publishes it; it "+
		"never publishes.", false),
		func(ctx context.Context, in saveIn) (any, error) {
			plan, err := planJSON(in.Plan)
			if err != nil {
				return nil, err
			}
			input := graph.PlanDraftInput{Plan: plan, Comment: &in.Comment, PlanID: &in.PlanID}
			if in.Base != "" {
				input.Base = &in.Base
			}
			rev, err := r.Mutation().CreatePlanDraft(ctx, input)
			if err != nil {
				return nil, err
			}
			out := toRevisionOut(*rev)
			if out.Parent != "" {
				diff, err := r.Query().Diff(ctx, out.Parent, &out.ID, nil)
				if err != nil {
					return nil, err
				}
				out.DiffLines = strings.Count(diff, "\n")
			}
			out.ReviewURL = reviewURL(ctx, r, out.ID)
			return out, nil
		})
}
