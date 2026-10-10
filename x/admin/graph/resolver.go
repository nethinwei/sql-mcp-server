package graph

import (
	"github.com/nethinwei/sql-mcp-server/core/bus"
	"github.com/nethinwei/sql-mcp-server/x/admin/accounts"
	"github.com/nethinwei/sql-mcp-server/x/admin/configapp"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
)

// RuntimeState is what the serving process reports about itself.
type RuntimeState struct {
	// Applied is the store revision being served; 0 when unknown.
	Applied  int64
	Watching bool
	// Pending is a published revision not applied; nil when none.
	Pending *bootstrap.StaleState
	// Execution is a snapshot of the execution bus; nil when unknown.
	Execution *bus.Stats
}

// Resolver is the root resolver. It holds no per-request state: the
// configuration service holds the business rules and the resolvers adapt
// GraphQL to it.
type Resolver struct {
	App      *configapp.Service
	Accounts accounts.Service
	// Status reports the serving process; nil reports nothing applied.
	Status func() RuntimeState
}
