package configapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/internal/kit"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/revisionops"
)

// Simulation is one authorization evaluated offline.
type Simulation struct {
	Allowed bool     `json:"allowed"`
	Reason  string   `json:"reason"`
	Fields  []string `json:"fields"`
	// Effective row filter in rowPolicies JSON form; null means every row.
	RowFilter any      `json:"rowFilter,omitempty"`
	Grants    []string `json:"grants"`
	// Set when no single grant covers the requested fields.
	FieldScopes [][]string `json:"fieldScopes,omitempty"`
}

// rolePrefix lets a simulation evaluate a bare role instead of a configured
// user.
const rolePrefix = "role:"

// Simulator evaluates the authorizations of one user or role of a
// configuration, without touching any database.
type Simulator struct {
	authz    rbac.Authorizer
	key      string
	subject  map[string]any
	disabled bool
}

// NewSimulator evaluates who, a configured user name or "role:<name>", in
// cfg; subject supplies attributes the user does not set.
func NewSimulator(cfg *config.Config, who string, subject map[string]any) (*Simulator, error) {
	s := &Simulator{subject: map[string]any{}}
	for k, v := range subject {
		s.subject[k] = v
	}
	if role, ok := strings.CutPrefix(who, rolePrefix); ok {
		s.key = strings.ToLower(role)
	} else {
		user, ok := cfg.Users[strings.ToLower(who)]
		if !ok {
			return nil, fmt.Errorf("unknown user %q (use %q to simulate a role)", who, rolePrefix+"<name>")
		}
		for k, v := range user.Subject {
			s.subject[k] = v
		}
		s.key, s.disabled = bootstrap.UserPrincipal(strings.ToLower(who)), user.Disabled
	}
	authz, err := bootstrap.OfflineAuthorizer(cfg)
	if err != nil {
		return nil, err
	}
	s.authz = authz
	return s, nil
}

// Evaluate evaluates action (read, aggregate, create, update, delete or
// execute) on an entity, for fields (none: the default projection).
func (s *Simulator) Evaluate(ctx context.Context, entityName, action string, fields []string) (*Simulation, error) {
	a, ok := entity.ParseAction(strings.ToLower(action))
	if !ok {
		return nil, fmt.Errorf("unknown action %q", action)
	}
	if s.disabled {
		return &Simulation{Reason: "user is disabled", Fields: []string{}, Grants: []string{}}, nil
	}
	dec, err := s.authz.Authorize(ctx, rbac.Request{
		Role: s.key, Entity: entityName, Action: a, Fields: fields, Subject: s.subject,
	})
	if err != nil {
		return nil, err
	}
	out := &Simulation{
		Allowed: dec.Allowed, Reason: dec.Reason, Fields: kit.OrEmpty(dec.Fields), Grants: kit.OrEmpty(dec.Grants),
		FieldScopes: dec.FieldScopes,
	}
	if dec.Allowed && dec.RowFilter != nil {
		out.RowFilter = config.FilterOf(dec.RowFilter)
	}
	return out, nil
}

// SimulateRequest is one authorization to evaluate.
type SimulateRequest struct {
	// Against selects the configuration.
	Against Against
	// User is a configured user name, or role:<name>.
	User, Entity, Action string
	Fields               []string
	Subject              map[string]any
}

// Simulate evaluates one authorization offline.
func (r *Service) Simulate(ctx context.Context, in SimulateRequest) (*Simulation, error) {
	if _, err := auth.RequireUnlimited(ctx, auth.PermRead); err != nil {
		return nil, err
	}
	cfg, err := r.SelectConfig(ctx, in.Against)
	if err != nil {
		return nil, err
	}
	s, err := NewSimulator(cfg, in.User, in.Subject)
	if err != nil {
		return nil, err
	}
	return s.Evaluate(ctx, in.Entity, in.Action, in.Fields)
}

// Diff is the unified diff, secrets redacted, from a revision to the
// configuration to is.
func (r *Service) Diff(ctx context.Context, from string, to Against) (string, error) {
	if _, err := auth.RequireUnlimited(ctx, auth.PermRead); err != nil {
		return "", err
	}
	a, err := r.Revision(ctx, from)
	if err != nil {
		return "", err
	}
	name := func(id int64) string { return fmt.Sprintf("revision %d", id) }
	if to.Workspace && to.Revision == nil {
		draft, err := r.draftOf(ctx, to)
		if err != nil {
			return "", err
		}
		if draft != nil {
			d, err := r.BuildDraft(ctx, *draft)
			if err != nil {
				return "", err
			}
			return revisionops.Diff(name(a.ID), a.Payload, "workspace", d.Payload)
		}
	}
	var b revision.Revision
	if to.Revision != nil {
		b, err = r.Revision(ctx, *to.Revision)
	} else {
		b, err = r.Store.Published(ctx)
	}
	if err != nil {
		return "", err
	}
	return revisionops.Diff(name(a.ID), a.Payload, name(b.ID), b.Payload)
}

// maxListed bounds one page of revisions or workspace log entries: a store
// lists everything for a limit of 0, which a caller must not reach.
const maxListed = 1000

// listLimit is limit within 1..maxListed.
func listLimit(limit int) int { return min(max(limit, 1), maxListed) }

// Revisions lists revisions, newest first, without their payloads: at least
// one and at most maxListed.
func (r *Service) Revisions(ctx context.Context, limit int) ([]revision.Revision, error) {
	if _, err := auth.RequireUnlimited(ctx, auth.PermRead); err != nil {
		return nil, err
	}
	return r.Store.List(ctx, listLimit(limit))
}
