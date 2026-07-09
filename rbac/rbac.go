package rbac

import (
	"context"
	"fmt"

	"github.com/nethinwei/sql-mcp-server/entity"
	"github.com/nethinwei/sql-mcp-server/relalg"
)

// Request describes an authorization query.
type Request struct {
	Role      string
	Entity    string
	Action    entity.Action
	Fields    []string         // requested fields; nil/empty means all visible
	Predicate relalg.Predicate // the request's own filter (for context, not mutated here)
}

// Decision is the authorization outcome. When Allowed, Fields is the
// projected set the caller may read, and RowFilter is the role's row-level
// predicate to AND with the request predicate.
type Decision struct {
	Allowed   bool
	Reason    string
	Fields    []string
	RowFilter relalg.Predicate
}

// Authorizer authorizes a request. Implementations must be safe for concurrent
// use (invariant I9).
type Authorizer interface {
	Authorize(ctx context.Context, req Request) (Decision, error)
}

// RoleAuthorizer authorizes against an immutable entity.Registry.
type RoleAuthorizer struct {
	registry *entity.Registry
}

// NewRoleAuthorizer returns an Authorizer backed by the given registry.
func NewRoleAuthorizer(reg *entity.Registry) *RoleAuthorizer {
	return &RoleAuthorizer{registry: reg}
}

// Authorize checks the role against the entity's RoleAccess, projects fields,
// and attaches the role's row-level filter. An unknown entity or unpermitted
// role yields Allowed=false (not an error).
func (a *RoleAuthorizer) Authorize(_ context.Context, req Request) (Decision, error) {
	res, ok := a.registry.Resolve(req.Entity)
	if !ok {
		return Decision{Allowed: false, Reason: fmt.Sprintf("entity %q not found", req.Entity)}, nil
	}
	if !roleAllowed(res.Entity.Role, req.Action, req.Role) {
		return Decision{Allowed: false, Reason: fmt.Sprintf("role %q not permitted to %s %q", req.Role, req.Action, req.Entity)}, nil
	}
	return Decision{
		Allowed:   true,
		Fields:    projectFields(res.Attributes, req.Fields),
		RowFilter: res.Entity.RowPolicies[req.Role],
	}, nil
}

func roleAllowed(access entity.RoleAccess, action entity.Action, role string) bool {
	for _, r := range access[action] {
		if r == role {
			return true
		}
	}
	return false
}

// projectFields returns the visible field names a request may read. With no
// requested fields, all non-excluded attributes are returned. Otherwise only
// requested fields that exist (by name or alias) and are visible are kept.
func projectFields(visible []entity.Attribute, requested []string) []string {
	if len(requested) == 0 {
		out := make([]string, 0, len(visible))
		for _, a := range visible {
			out = append(out, a.Name)
		}
		return out
	}
	allowed := make(map[string]bool, len(visible)*2)
	for _, a := range visible {
		allowed[a.Name] = true
		if a.Alias != "" {
			allowed[a.Alias] = true
		}
	}
	out := make([]string, 0, len(requested))
	for _, f := range requested {
		if allowed[f] {
			out = append(out, f)
		}
	}
	return out
}
