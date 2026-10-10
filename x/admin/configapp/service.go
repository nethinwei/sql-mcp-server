// Package configapp is the configuration service of the admin plane: the
// scanned catalog, schema scans and scan batches, plans and their previews,
// plan templates, pending work and administrators' MCP tokens. It checks who
// may do what (the signed-in principal on the context, with its permissions
// and datasources) and holds every business rule; the admin API (GraphQL)
// and the configuration MCP endpoint only adapt their requests and results.
package configapp

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/core/workspace"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/configedit"
	"github.com/nethinwei/sql-mcp-server/x/configstore"
	"github.com/nethinwei/sql-mcp-server/x/revisionops"
)

// PlanStore keeps plan templates, the administrators' tokens for the
// configuration MCP endpoint and the state of pending work, outside
// configuration revisions.
type PlanStore interface {
	PlanTemplates(ctx context.Context) ([]configstore.PlanTemplate, error)
	PlanTemplateVersions(ctx context.Context, name string) ([]configstore.PlanTemplate, error)
	PutPlanTemplate(ctx context.Context, t configstore.PlanTemplate, expected int) (configstore.PlanTemplate, error)
	DeletePlanTemplate(ctx context.Context, name string) error
	AdminTokens(ctx context.Context, username string) ([]configstore.AdminToken, error)
	AdminTokenByHash(ctx context.Context, hash string) (configstore.AdminToken, bool, error)
	CreateAdminToken(ctx context.Context, t configstore.AdminToken) (configstore.AdminToken, error)
	DeleteAdminToken(ctx context.Context, username, id string) error
	TouchAdminToken(ctx context.Context, id string, at time.Time) error
	PendingStates(ctx context.Context) ([]configstore.PendingState, error)
	PendingLog(ctx context.Context, key string) ([]configstore.PendingState, error)
	PutPendingState(ctx context.Context, p configstore.PendingState) (configstore.PendingState, error)
	workspace.Store
}

// Introspection runs fn with the introspector of datasource, reading the
// database that database (its configuration) names: what is read is what the
// caller took from its configuration, even before the runtime applies it.
type Introspection func(
	ctx context.Context, datasource string, database config.DatabaseConfig, fn func(introspect.Introspector) error,
) error

// Service is the configuration service. It holds no per-request state.
type Service struct {
	Store      revision.Store
	Introspect Introspection
	// Scans runs schema scans in the background and keeps their results;
	// nil refuses them.
	Scans *Scans
	// Plans keeps plan templates, MCP tokens and pending states; nil refuses
	// them.
	Plans PlanStore
	// Capabilities reports the serving snapshot's capabilities; nil reports
	// none.
	Capabilities func() bootstrap.EntityCapabilities
	// WorkspaceEdit reads the sections of a workspace (the console's JSON)
	// as an edit of its base, and WorkspaceSections writes a configuration
	// (with its payload) as such sections; the admin API provides both, with
	// the types the console edits.
	WorkspaceEdit     func(sections []byte) (configedit.Edit, error)
	WorkspaceSections func(cfg *config.Config, payload []byte) ([]byte, error)
	// previews keeps recent plan previews.
	previews previews
}

// Draft is a configuration being edited: a base revision with sections
// replaced.
type Draft struct {
	Base string
	Edit configedit.Edit
}

// ErrPlanChanged: a plan is not the one its preview ID names, or changed
// while it was checked.
var ErrPlanChanged = errors.New("the plan is not the one previewed, or its preview expired and what it evaluates " +
	"to changed since (a publish, a workspace edit or a new scan): preview it again")

func (r *Service) now() time.Time {
	if r.Scans != nil {
		return r.Scans.clock()
	}
	return time.Now()
}

func (r *Service) capabilities() bootstrap.EntityCapabilities {
	if r.Capabilities == nil {
		return nil
	}
	return r.Capabilities()
}

// Ops returns the revision operations shared with the `store` CLI.
func (r *Service) Ops() revisionops.Service {
	return revisionops.Service{Store: r.Store, Via: "admin-api"}
}

// ParseID reads a revision ID.
func ParseID(id string) (int64, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid revision id %q", id)
	}
	return n, nil
}

// Revision returns the revision with an ID.
func (r *Service) Revision(ctx context.Context, id string) (revision.Revision, error) {
	n, err := ParseID(id)
	if err != nil {
		return revision.Revision{}, err
	}
	return r.Store.Get(ctx, n)
}

// Built is a merged, validated and normalized configuration.
type Built struct {
	Base    revision.Revision
	Config  *config.Config
	Payload []byte
}

// BuildDraft applies d to its base revision through configedit, the same
// chain as `store import`: decode, defaults, validation, store secret rule and
// the deterministic encoding.
func (r *Service) BuildDraft(ctx context.Context, d Draft) (Built, error) {
	base, err := r.Revision(ctx, d.Base)
	if err != nil {
		return Built{}, err
	}
	if err := base.Verify(); err != nil {
		return Built{}, fmt.Errorf("base revision %d: %w", base.ID, err)
	}
	cfg, payload, err := configedit.Apply(base.Payload, d.Edit)
	if err != nil {
		return Built{}, err
	}
	return Built{Base: base, Config: cfg, Payload: payload}, nil
}

// SelectConfig selects a configuration: a draft, a revision, or, when both
// are nil, the published revision.
func (r *Service) SelectConfig(ctx context.Context, draft *Draft, id *string) (*config.Config, error) {
	if draft != nil && id != nil {
		return nil, errors.New("pass either draft or revision, not both")
	}
	if draft != nil {
		d, err := r.BuildDraft(ctx, *draft)
		if err != nil {
			return nil, err
		}
		return d.Config, nil
	}
	var rev revision.Revision
	var err error
	if id != nil {
		rev, err = r.Revision(ctx, *id)
	} else {
		rev, err = r.Store.Published(ctx)
	}
	if err != nil {
		return nil, err
	}
	return bootstrap.LoadRevision(rev)
}

// RestartChanges compares a configuration with the published one.
func (r *Service) RestartChanges(ctx context.Context, next *config.Config) ([]string, error) {
	current, err := r.Store.Published(ctx)
	if errors.Is(err, revision.ErrNoPublished) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	cur, err := bootstrap.LoadRevision(current)
	if err != nil {
		return nil, err
	}
	return orEmpty(bootstrap.RestartChanges(cur, next)), nil
}

// DiscardDraft discards a draft, saved by anyone, an agent included: it
// stays in the history and is never published.
func (r *Service) DiscardDraft(ctx context.Context, id string) (revision.Revision, error) {
	who, err := auth.RequireUnlimited(ctx, auth.PermWrite)
	if err != nil {
		return revision.Revision{}, err
	}
	n, err := ParseID(id)
	if err != nil {
		return revision.Revision{}, err
	}
	return r.Ops().Discard(ctx, n, who.Username)
}

// PendingDraft is a draft with the revision it builds on: the first up its
// parents that is not a draft (0: none).
type PendingDraft struct {
	Revision revision.Revision
	BasedOn  int64
}

// Drafts lists every draft, newest first, with what each builds on, worked
// out over the whole history.
func (r *Service) Drafts(ctx context.Context) ([]PendingDraft, error) {
	if _, err := auth.RequireUnlimited(ctx, auth.PermRead); err != nil {
		return nil, err
	}
	all, err := r.Store.List(ctx, 0)
	if err != nil {
		return nil, err
	}
	// A parent is older than its children: oldest first, a draft's parent
	// already has its own answer.
	sorted := slices.SortedFunc(slices.Values(all), func(a, b revision.Revision) int { return cmp.Compare(a.ID, b.ID) })
	basedOn := make(map[int64]int64, len(sorted))
	for _, rev := range sorted {
		if !rev.State.Unpublished() {
			basedOn[rev.ID] = rev.ID
		} else if at, ok := basedOn[rev.ParentID]; ok && rev.ParentID < rev.ID {
			basedOn[rev.ID] = at
		} else {
			basedOn[rev.ID] = rev.ParentID
		}
	}
	out := []PendingDraft{}
	for _, rev := range all {
		if rev.State == revision.StateDraft {
			out = append(out, PendingDraft{Revision: rev, BasedOn: basedOn[rev.ID]})
		}
	}
	return out, nil
}

// Validate builds a draft and reports whether it validates, what of it
// needs a restart and what the serving connections cannot do.
func (r *Service) Validate(ctx context.Context, d Draft) (*Validation, error) {
	if _, err := auth.RequireUnlimited(ctx, auth.PermRead); err != nil {
		return nil, err
	}
	built, err := r.BuildDraft(ctx, d)
	if err != nil {
		msg := err.Error()
		return &Validation{Error: &msg, RestartRequired: []string{}, Warnings: []string{}}, nil
	}
	changes, err := r.RestartChanges(ctx, built.Config)
	if err != nil {
		return nil, err
	}
	hash := revision.Hash(built.Payload)
	return &Validation{Ok: true, ContentHash: &hash, RestartRequired: changes,
		Warnings: orEmpty(bootstrap.CapabilityWarnings(built.Config, r.capabilities()))}, nil
}

func hasDatasource(cfg *config.Config, name string) bool {
	_, ok := cfg.Databases[name]
	return ok
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefBool(b *bool) bool { return b != nil && *b }

func derefInt(n *int, fallback int) int {
	if n == nil {
		return fallback
	}
	return *n
}

// optional is s, nil when empty.
func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// orEmpty is s, never nil.
func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
