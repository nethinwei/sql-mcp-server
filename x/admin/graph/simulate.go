package graph

import (
	"context"
	"fmt"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
)

var graphActions = map[Action]entity.Action{
	ActionRead: entity.ActionRead, ActionCreate: entity.ActionCreate, ActionUpdate: entity.ActionUpdate,
	ActionDelete: entity.ActionDelete, ActionExecute: entity.ActionExecute, ActionAggregate: entity.ActionAggregate,
}

// rolePrefix lets simulate evaluate a bare role instead of a configured user.
const rolePrefix = "role:"

// principal is the identity a simulation evaluates.
type principal struct {
	key      string
	subject  map[string]any
	disabled bool
}

// resolvePrincipal maps who, a configured user name or "role:<name>", to its
// authorization key. subject supplies attributes the user does not set.
func resolvePrincipal(cfg *config.Config, who string, subject map[string]any) (principal, error) {
	merged := map[string]any{}
	for k, v := range subject {
		merged[k] = v
	}
	if role, ok := strings.CutPrefix(who, rolePrefix); ok {
		return principal{key: strings.ToLower(role), subject: merged}, nil
	}
	user, ok := cfg.Users[strings.ToLower(who)]
	if !ok {
		return principal{}, fmt.Errorf("unknown user %q (use %q to simulate a role)", who, rolePrefix+"<name>")
	}
	for k, v := range user.Subject {
		merged[k] = v
	}
	return principal{key: bootstrap.UserPrincipal(strings.ToLower(who)), subject: merged, disabled: user.Disabled}, nil
}

// simulate evaluates one authorization against cfg without touching any
// database.
func simulate(
	ctx context.Context,
	cfg *config.Config,
	who, entityName string,
	action Action,
	fields []string,
	subject map[string]any,
) (*Simulation, error) {
	p, err := resolvePrincipal(cfg, who, subject)
	if err != nil {
		return nil, err
	}
	authz, err := bootstrap.OfflineAuthorizer(cfg)
	if err != nil {
		return nil, err
	}
	return evaluate(ctx, authz, p, entityName, action, fields)
}

// visibility evaluates every entity and applicable action for one principal
// with a single authorizer.
func visibility(
	ctx context.Context, cfg *config.Config, who string, subject map[string]any,
) ([]EntityVisibility, error) {
	p, err := resolvePrincipal(cfg, who, subject)
	if err != nil {
		return nil, err
	}
	authz, err := bootstrap.OfflineAuthorizer(cfg)
	if err != nil {
		return nil, err
	}
	out := make([]EntityVisibility, 0, len(cfg.Entities))
	for _, e := range cfg.Entities {
		actions := []Action{ActionRead, ActionAggregate, ActionCreate, ActionUpdate, ActionDelete}
		if e.Kind == "procedure" {
			actions = []Action{ActionExecute}
		}
		ev := EntityVisibility{Entity: e.Name, Actions: make([]ActionVisibility, 0, len(actions))}
		for _, a := range actions {
			sim, err := evaluate(ctx, authz, p, e.Name, a, nil)
			if err != nil {
				return nil, err
			}
			ev.Actions = append(ev.Actions, ActionVisibility{Action: a, Result: sim})
		}
		out = append(out, ev)
	}
	return out, nil
}

func evaluate(
	ctx context.Context, authz rbac.Authorizer, p principal, entityName string, action Action, fields []string,
) (*Simulation, error) {
	if p.disabled {
		return &Simulation{Reason: "user is disabled", Fields: []string{}, Grants: []string{}}, nil
	}
	dec, err := authz.Authorize(ctx, rbac.Request{
		Role: p.key, Entity: entityName, Action: graphActions[action], Fields: fields, Subject: p.subject,
	})
	if err != nil {
		return nil, err
	}
	out := &Simulation{
		Allowed: dec.Allowed, Reason: dec.Reason, Fields: orEmpty(dec.Fields), Grants: orEmpty(dec.Grants),
		FieldScopes: dec.FieldScopes,
	}
	if dec.Allowed && dec.RowFilter != nil {
		out.RowFilter = predicateJSON(dec.RowFilter)
	}
	return out, nil
}

// predicateJSON renders a predicate in the rowPolicies JSON form.
func predicateJSON(p relalg.Predicate) any {
	switch x := p.(type) {
	case relalg.Condition:
		return map[string]any{"op": string(x.Op), "field": x.Field, "value": x.Value}
	case relalg.And:
		return map[string]any{"and": predicateList(x.Preds)}
	case relalg.Or:
		return map[string]any{"or": predicateList(x.Preds)}
	case relalg.Not:
		return map[string]any{"not": predicateJSON(x.P)}
	}
	return fmt.Sprintf("%v", p)
}

func predicateList(ps []relalg.Predicate) []any {
	out := make([]any, 0, len(ps))
	for _, p := range ps {
		out = append(out, predicateJSON(p))
	}
	return out
}
