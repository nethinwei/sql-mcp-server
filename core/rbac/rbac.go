package rbac

import (
	"context"
	"fmt"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

// Request describes an authorization query.
type Request struct {
	Role        string
	Entity      string
	Action      entity.Action
	Fields      []string         // requested fields; nil/empty means all visible
	ReadFields  []string         // fields used by projections, filters, grouping, or aggregates
	WriteFields []string         // fields assigned by create/update
	Subject     map[string]any   // caller attributes for ${subject.x} row-policy resolution
	Predicate   relalg.Predicate // the request's own filter (for context, not mutated here)
}

// Decision is the authorization outcome. When Allowed, Fields is the
// projected set the caller may read, and RowFilter is the effective row-level
// predicate (covering grants ORed, tenant policy ANDed) to AND with the request
// predicate. Grants lists the covering grant IDs for decision traces.
type Decision struct {
	Allowed   bool
	Reason    string
	Fields    []string
	RowFilter relalg.Predicate
	Grants    []string
	// FieldScopes is set on an ambiguous-field-scope denial: each entry is a
	// field set one grant covers, so the caller can retry with explicit fields.
	FieldScopes [][]string
}

// Authorizer authorizes a request. Implementations must be safe for concurrent
// use (invariant I9).
type Authorizer interface {
	Authorize(ctx context.Context, req Request) (Decision, error)
}

// Grant is one compiled entity permission. Nil Fields grants every visible
// field; nil Rows grants every row.
type Grant struct {
	ID      string
	Actions []entity.Action
	Fields  *entity.FieldPermissions
	Rows    relalg.Predicate
}

func (g Grant) allows(action entity.Action) bool {
	for _, a := range g.Actions {
		if a == action {
			return true
		}
	}
	return false
}

// Principal is a configured user: its roles and direct grants by entity.
type Principal struct {
	Roles  []string
	Grants map[string][]Grant
}

// Policy is the compiled top-level authorization model. Roles maps a role to
// its grants by entity; Principals maps a principal key (for example
// "user:alice") to a configured user. A principal key without an entry is a
// role name, which keeps entity-level roles/fieldACL/rowPolicies working.
type Policy struct {
	Roles      map[string]map[string][]Grant
	Principals map[string]Principal
}

// GrantAuthorizer authorizes against an immutable entity.Registry and Policy.
type GrantAuthorizer struct {
	registry *entity.Registry
	policy   Policy
}

// NewGrantAuthorizer returns an Authorizer for the registry and policy.
func NewGrantAuthorizer(reg *entity.Registry, policy Policy) *GrantAuthorizer {
	return &GrantAuthorizer{registry: reg, policy: policy}
}

// NewRoleAuthorizer returns an Authorizer that only uses entity-level role
// configuration.
func NewRoleAuthorizer(reg *entity.Registry) *GrantAuthorizer {
	return NewGrantAuthorizer(reg, Policy{})
}

// Authorize applies the covering rule: only grants that allow the action and
// cover every field the request uses contribute, their row filters are ORed,
// and the entity tenant policy is always ANDed. Every returned row and column
// is therefore covered by at least one single grant. An unknown entity or an
// unpermitted principal yields Allowed=false (not an error).
func (a *GrantAuthorizer) Authorize(_ context.Context, req Request) (Decision, error) {
	req.Role = NormalizeRole(req.Role)
	res, ok := a.registry.Resolve(req.Entity)
	if !ok {
		return Decision{Allowed: false, Reason: fmt.Sprintf("entity %q not found", req.Entity)}, nil
	}
	candidates := a.candidates(res.Entity, req.Role, req.Action)
	if len(candidates) == 0 {
		return Decision{
			Allowed: false,
			Reason:  fmt.Sprintf("role %q not permitted to %s %q", req.Role, req.Action, req.Entity),
		}, nil
	}
	readFields, writeFields := req.ReadFields, req.WriteFields
	if len(readFields) == 0 && len(writeFields) == 0 {
		switch req.Action {
		case entity.ActionCreate, entity.ActionUpdate:
			writeFields = req.Fields
		default:
			readFields = req.Fields
		}
	}
	if req.Action == entity.ActionRead && len(req.Fields) == 0 && allRestricted(candidates) &&
		len(unionProjection(res.Attributes, nil, candidates)) == 0 {
		return Decision{Allowed: false, Reason: fmt.Sprintf("role %q has no readable fields", req.Role)}, nil
	}
	covering := coveringGrants(candidates, readFields, writeFields, res.Attributes)
	if len(covering) == 0 {
		return deniedFieldScope(req, candidates, readFields, writeFields, res.Attributes), nil
	}
	projected := unionProjection(res.Attributes, req.Fields, covering)
	covering = coveringGrants(covering, projected, nil, res.Attributes)
	if len(covering) == 0 {
		return ambiguousFieldScope(req, candidates, res.Attributes), nil
	}
	filter, ids := effectiveRowFilter(covering, res.Entity.TenantPolicy)
	return Decision{
		Allowed:   true,
		Fields:    projected,
		RowFilter: resolveSubject(filter, req.Subject),
		Grants:    ids,
	}, nil
}

// effectiveRowFilter ORs the covering grants' rows (TRUE when any grant is
// unrestricted) and ANDs the tenant policy, returning the grant IDs used.
func effectiveRowFilter(covering []Grant, tenant relalg.Predicate) (relalg.Predicate, []string) {
	rows := make([]relalg.Predicate, 0, len(covering))
	ids := make([]string, 0, len(covering))
	unrestricted := false
	for _, g := range covering {
		ids = append(ids, g.ID)
		if g.Rows == nil {
			unrestricted = true
			continue
		}
		rows = append(rows, g.Rows)
	}
	var filter relalg.Predicate
	if !unrestricted {
		filter = orPredicates(rows)
	}
	if tenant != nil {
		filter = andPredicates(tenant, filter)
	}
	return filter, ids
}

// candidates returns the grants on e that allow action for the principal: the
// principal's direct grants plus, for each of its roles, top-level role grants
// and the entity-level role grant.
func (a *GrantAuthorizer) candidates(e entity.Entity, principal string, action entity.Action) []Grant {
	roles := []string{principal}
	var out []Grant
	if p, ok := a.policy.Principals[principal]; ok {
		roles = p.Roles
		for _, g := range p.Grants[e.Name] {
			if g.allows(action) {
				out = append(out, g)
			}
		}
	}
	for _, role := range roles {
		for _, g := range a.policy.Roles[role][e.Name] {
			if g.allows(action) {
				out = append(out, g)
			}
		}
		if g, ok := entityRoleGrant(e, role); ok && g.allows(action) {
			out = append(out, g)
		}
	}
	return out
}

// entityRoleGrant compiles entity-level roles/fieldACL/rowPolicies for one
// role into a single grant.
func entityRoleGrant(e entity.Entity, role string) (Grant, bool) {
	var actions []entity.Action
	for action, roles := range e.Role {
		for _, r := range roles {
			if NormalizeRole(r) == role {
				actions = append(actions, action)
				break
			}
		}
	}
	if len(actions) == 0 {
		return Grant{}, false
	}
	g := Grant{ID: "entity:" + e.Name + ":" + role, Actions: actions, Rows: rowPolicyForRole(e.RowPolicies, role)}
	if acl, configured := fieldAccessForRole(e.FieldAccess, role); configured {
		g.Fields = &acl
	}
	return g, true
}

func coveringGrants(grants []Grant, read, write []string, visible []entity.Attribute) []Grant {
	out := make([]Grant, 0, len(grants))
	for _, g := range grants {
		if g.Fields == nil {
			out = append(out, g)
			continue
		}
		if firstDenied(read, g.Fields.Read, visible) == "" && firstDenied(write, g.Fields.Write, visible) == "" {
			out = append(out, g)
		}
	}
	return out
}

// deniedFieldScope explains why no candidate covers the request: a field no
// candidate can read or write at all, or otherwise an ambiguous combination.
func deniedFieldScope(req Request, candidates []Grant, read, write []string, visible []entity.Attribute) Decision {
	for _, f := range read {
		if len(coveringGrants(candidates, []string{f}, nil, visible)) == 0 {
			return Decision{Allowed: false, Reason: fmt.Sprintf("field %q is not readable by role %q", f, req.Role)}
		}
	}
	for _, f := range write {
		if len(coveringGrants(candidates, nil, []string{f}, visible)) == 0 {
			return Decision{Allowed: false, Reason: fmt.Sprintf("field %q is not writable by role %q", f, req.Role)}
		}
	}
	return ambiguousFieldScope(req, candidates, visible)
}

func ambiguousFieldScope(req Request, candidates []Grant, visible []entity.Attribute) Decision {
	scopes := make([][]string, 0, len(candidates))
	for _, g := range candidates {
		scopes = append(scopes, grantProjection(visible, nil, g))
	}
	return Decision{
		Allowed:     false,
		Reason:      fmt.Sprintf("no single grant of role %q covers the requested fields of %q", req.Role, req.Entity),
		FieldScopes: scopes,
	}
}

// unionProjection returns the requested (or, when none, all) visible fields
// readable by at least one grant, in attribute order.
func unionProjection(visible []entity.Attribute, requested []string, grants []Grant) []string {
	seen := make(map[string]bool)
	for _, g := range grants {
		for _, f := range grantProjection(visible, requested, g) {
			seen[f] = true
		}
	}
	out := make([]string, 0, len(seen))
	for _, f := range projectFields(visible, requested) {
		if seen[f] {
			out = append(out, f)
		}
	}
	return out
}

func grantProjection(visible []entity.Attribute, requested []string, g Grant) []string {
	if g.Fields == nil {
		return projectFields(visible, requested)
	}
	return projectFieldsForACL(visible, requested, *g.Fields)
}

func allRestricted(grants []Grant) bool {
	for _, g := range grants {
		if g.Fields == nil {
			return false
		}
	}
	return true
}

func orPredicates(preds []relalg.Predicate) relalg.Predicate {
	if len(preds) == 1 {
		return preds[0]
	}
	return relalg.Or{Preds: preds}
}

func andPredicates(tenant, filter relalg.Predicate) relalg.Predicate {
	if filter == nil {
		return tenant
	}
	return relalg.And{Preds: []relalg.Predicate{tenant, filter}}
}

// NormalizeRole returns the canonical role identity used by authorization,
// transport session binding, and configuration.
func NormalizeRole(role string) string {
	return strings.ToLower(strings.TrimSpace(role))
}

func fieldAccessForRole(access entity.FieldAccess, role string) (entity.FieldPermissions, bool) {
	if acl, ok := access[role]; ok {
		return acl, true
	}
	for configured, acl := range access {
		if NormalizeRole(configured) == role {
			return acl, true
		}
	}
	return entity.FieldPermissions{}, false
}

func rowPolicyForRole(policies entity.RowPolicies, role string) relalg.Predicate {
	if policy, ok := policies[role]; ok {
		return policy
	}
	for configured, policy := range policies {
		if NormalizeRole(configured) == role {
			return policy
		}
	}
	return nil
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

func projectFieldsForACL(visible []entity.Attribute, requested []string, acl entity.FieldPermissions) []string {
	allowed := make(map[string]bool, len(acl.Read))
	for _, f := range acl.Read {
		allowed[f] = true
	}
	projected := projectFields(visible, requested)
	out := projected[:0]
	for _, f := range projected {
		if fieldAllowed(f, allowed, visible) {
			out = append(out, f)
		}
	}
	return out
}

func firstDenied(requested, configured []string, visible []entity.Attribute) string {
	allowed := make(map[string]bool, len(configured))
	for _, f := range configured {
		allowed[f] = true
	}
	for _, f := range requested {
		if !fieldAllowed(f, allowed, visible) {
			return f
		}
	}
	return ""
}

func fieldAllowed(field string, allowed map[string]bool, visible []entity.Attribute) bool {
	if allowed[field] {
		return true
	}
	for _, a := range visible {
		if (a.Name == field || a.Alias == field) && (allowed[a.Name] || a.Alias != "" && allowed[a.Alias]) {
			return true
		}
	}
	return false
}

// resolveSubject walks a row-policy predicate and replaces ${subject.attr}
// placeholder values with the request subject's attribute. A placeholder with
// no matching attribute resolves to nil, which matches no rows — fail-closed
// for a missing subject rather than exposing every tenant's data. The row
// policy is stored immutably; resolveSubject returns fresh nodes.
func resolveSubject(p relalg.Predicate, subject map[string]any) relalg.Predicate {
	switch pp := p.(type) {
	case relalg.Condition:
		if s, ok := pp.Value.(string); ok {
			if attr, isPlaceholder := subjectPlaceholder(s); isPlaceholder {
				pp.Value = subject[attr]
			}
		}
		return pp
	case relalg.And:
		out := make([]relalg.Predicate, len(pp.Preds))
		for i, q := range pp.Preds {
			out[i] = resolveSubject(q, subject)
		}
		return relalg.And{Preds: out}
	case relalg.Or:
		out := make([]relalg.Predicate, len(pp.Preds))
		for i, q := range pp.Preds {
			out[i] = resolveSubject(q, subject)
		}
		return relalg.Or{Preds: out}
	case relalg.Not:
		return relalg.Not{P: resolveSubject(pp.P, subject)}
	}
	return p
}

// subjectPlaceholder parses "${subject.attr}" into ("attr", true).
func subjectPlaceholder(s string) (string, bool) {
	const prefix, suffix = "${subject.", "}"
	if strings.HasPrefix(s, prefix) && strings.HasSuffix(s, suffix) {
		return s[len(prefix) : len(s)-len(suffix)], true
	}
	return "", false
}
