package graph

import (
	"context"

	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/revisionops"
)

// ops returns the revision operations shared with the `store` CLI.
func (r *Resolver) ops() revisionops.Service { return r.App.Ops() }

func (r *Resolver) revisionByID(ctx context.Context, id string) (revision.Revision, error) {
	return r.App.Revision(ctx, id)
}
