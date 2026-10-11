package graph

import (
	"context"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
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
