package graph

import (
	"context"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/x/admin/configapp"
)

var graphActions = map[Action]entity.Action{
	ActionRead: entity.ActionRead, ActionCreate: entity.ActionCreate, ActionUpdate: entity.ActionUpdate,
	ActionDelete: entity.ActionDelete, ActionExecute: entity.ActionExecute, ActionAggregate: entity.ActionAggregate,
}

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
		actions := []Action{ActionRead, ActionAggregate, ActionCreate, ActionUpdate, ActionDelete}
		if e.Kind == "procedure" {
			actions = []Action{ActionExecute}
		}
		ev := EntityVisibility{Entity: e.ID(), Actions: make([]ActionVisibility, 0, len(actions))}
		for _, a := range actions {
			result, err := sim.Evaluate(ctx, e.ID(), graphActions[a].String(), nil)
			if err != nil {
				return nil, err
			}
			ev.Actions = append(ev.Actions, ActionVisibility{Action: a, Result: result})
		}
		out = append(out, ev)
	}
	return out, nil
}
