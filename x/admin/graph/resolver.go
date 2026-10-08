package graph

import (
	"context"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/configstore"
)

// AccountStore persists administrator accounts.
type AccountStore interface {
	CreateAdmin(ctx context.Context, a configstore.AdminAccount) (configstore.AdminAccount, error)
	GetAdmin(ctx context.Context, username string) (configstore.AdminAccount, error)
	ListAdmins(ctx context.Context) ([]configstore.AdminAccount, error)
	UpdateAdmin(ctx context.Context, a configstore.AdminAccount) (configstore.AdminAccount, error)
}

// Introspector discovers the tables of a configured datasource.
type Introspector func(ctx context.Context, datasource string, schemas []string) ([]entity.Entity, error)

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
	Accounts   AccountStore
	Introspect Introspector
	// Status reports the serving process; nil reports nothing applied.
	Status func() RuntimeState
}
