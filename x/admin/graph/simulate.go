package graph

import (
	"context"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/internal/kit"
	"github.com/nethinwei/sql-mcp-server/x/admin/configapp"
)

// visibility evaluates every entity and applicable action for one principal
// with a single authorizer.
func visibility(
	ctx context.Context, cfg *config.Config, who string, subject map[string]any,
) ([]EntityVisibility, error) {
	sim, err := configapp.NewSimulator(cfg, who, subject)
	if err != nil {
		return nil, err
	}
	out := make([]EntityVisibility, 0, len(cfg.Entities))
	for _, e := range cfg.Entities {
		actions := []entity.Action{
			entity.ActionRead, entity.ActionAggregate, entity.ActionCreate, entity.ActionUpdate, entity.ActionDelete,
		}
		if e.Kind == "procedure" {
			actions = []entity.Action{entity.ActionExecute}
		}
		ev := EntityVisibility{Entity: e.ID(), Actions: make([]ActionVisibility, 0, len(actions))}
		for _, a := range actions {
			result, err := sim.Evaluate(ctx, e.ID(), a.String(), nil)
			if err != nil {
				return nil, err
			}
			// The GraphQL Action enum spells the action in upper case.
			ev.Actions = append(ev.Actions, ActionVisibility{Action: strings.ToUpper(a.String()), Result: result})
		}
		out = append(out, ev)
	}
	return out, nil
}

// maxVisibilityPage bounds one page of visibility.
const maxVisibilityPage = 200

// visibilityPage counts every entity's visibility and returns the matching
// ones from offset on, so a console with thousands of entities receives one
// page instead of every entity × action.
func visibilityPage(all []EntityVisibility, filter *VisibilityFilter, offset, limit int) *VisibilityPage {
	search, reachableOnly := "", false
	if filter != nil {
		search = strings.ToLower(kit.Deref(filter.Search))
		reachableOnly = filter.ReachableOnly != nil && *filter.ReachableOnly
	}
	page := &VisibilityPage{Total: len(all), Actions: []string{}, Items: []EntityVisibility{}}
	applies := map[string]bool{}
	var matched []EntityVisibility
	for _, ev := range all {
		reachable, allowed := false, false
		for _, a := range ev.Actions {
			applies[a.Action] = true
			ok := a.Result != nil && a.Result.Allowed
			allowed = allowed || ok
			reachable = reachable || ok || hasFieldScope(a.Result)
		}
		if reachable {
			page.Reachable++
			if !allowed {
				page.NeedsFields++
			}
		}
		if (!reachableOnly || reachable) && strings.Contains(strings.ToLower(ev.Entity), search) {
			matched = append(matched, ev)
		}
	}
	for _, name := range []string{"READ", "AGGREGATE", "CREATE", "UPDATE", "DELETE", "EXECUTE"} {
		if applies[name] {
			page.Actions = append(page.Actions, name)
		}
	}
	page.Matched = len(matched)
	limit = min(max(limit, 1), maxVisibilityPage)
	if offset = max(offset, 0); offset < len(matched) {
		page.Items = matched[offset:min(offset+limit, len(matched))]
	}
	return page
}

// hasFieldScope reports whether a denied result names fields that one grant
// covers: the call is allowed when it selects fields within that scope.
func hasFieldScope(s *configapp.Simulation) bool {
	if s == nil {
		return false
	}
	for _, scope := range s.FieldScopes {
		if len(scope) > 0 {
			return true
		}
	}
	return false
}
