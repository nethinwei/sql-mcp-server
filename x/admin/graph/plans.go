package graph

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/x/admin/configapp"
	"github.com/nethinwei/sql-mcp-server/x/configplan"
)

// decodeScope reads a plan scope from its GraphQL JSON value.
func decodeScope(v any) (configplan.Scope, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return configplan.Scope{}, err
	}
	var scope configplan.Scope
	if err := json.Unmarshal(data, &scope); err != nil {
		return configplan.Scope{}, fmt.Errorf("scope: %w", err)
	}
	return scope, nil
}

// toPlanPreview shows a preview with the entities, roles and users it
// changes as the console edits them.
func toPlanPreview(p *configapp.Preview) *PlanPreview {
	proposal := p.Proposal
	out := &PlanPreview{
		PlanID: p.PlanID, Report: p.Report, Entities: make([]Entity, 0, len(proposal.Entities)),
		Added: orEmpty(proposal.Added), Validation: p.Validation,
	}
	for _, e := range proposal.Entities {
		out.Entities = append(out.Entities, toEntity(e))
	}
	canonical := canonicalGrants(p.Base, proposal.Config)
	out.Roles = slices.DeleteFunc(toRoles(proposal.Config), func(role Role) bool {
		_, ok := proposal.Roles[role.Name]
		canonical(role.Grants)
		return !ok
	})
	out.Users = slices.DeleteFunc(toUsers(proposal.Config), func(u User) bool {
		_, ok := proposal.Users[u.Name]
		canonical(u.Grants)
		return !ok
	})
	return out
}

// canonicalGrants names the entity of each grant by its ID, as base resolved
// the reference (or, for a grant the plan added, the outcome), so the console
// can rewrite references once the entities are in its workspace.
func canonicalGrants(base, outcome *config.Config) func([]Grant) {
	before, after := config.NewEntityRefs(base.Entities), config.NewEntityRefs(outcome.Entities)
	return func(grants []Grant) {
		for i, g := range grants {
			if e, err := before.Resolve(g.Entity); err == nil {
				grants[i].Entity = e.ID()
			} else if e, err := after.Resolve(g.Entity); err == nil {
				grants[i].Entity = e.ID()
			}
		}
	}
}
