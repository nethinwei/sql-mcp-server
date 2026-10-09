package graph

import (
	"context"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/admin/accounts"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/configstore"
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
}

// Introspection runs fn with the introspector of datasource, reading the
// database that database (its configuration) names: what is read is what the
// caller took from its configuration, even before the runtime applies it.
type Introspection func(
	ctx context.Context, datasource string, database config.DatabaseConfig, fn func(introspect.Introspector) error,
) error

// RuntimeState is what the serving process reports about itself.
type RuntimeState struct {
	// Applied is the store revision being served; 0 when unknown.
	Applied  int64
	Watching bool
	// Pending is a published revision not applied; nil when none.
	Pending *bootstrap.StaleState
}

// Resolver is the root resolver. It holds no per-request state.
type Resolver struct {
	Store      revision.Store
	Accounts   accounts.Service
	Introspect Introspection
	// Scans runs schema scans in the background and keeps their results;
	// nil refuses them.
	Scans *Scans
	// Plans keeps plan templates and MCP tokens; nil refuses them.
	Plans PlanStore
	// Status reports the serving process; nil reports nothing applied.
	Status func() RuntimeState
	// Capabilities reports the serving snapshot's capabilities; nil reports
	// none.
	Capabilities func() bootstrap.EntityCapabilities
}
