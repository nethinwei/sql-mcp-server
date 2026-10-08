package graph

import (
	"context"

	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/admin/accounts"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
)

// Introspection runs fn with the introspector of a connected datasource.
type Introspection func(ctx context.Context, datasource string, fn func(introspect.Introspector) error) error

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
	// Status reports the serving process; nil reports nothing applied.
	Status func() RuntimeState
}
