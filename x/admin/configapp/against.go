package configapp

import (
	"context"

	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
)

// Against is the configuration a read is about: the caller's workspace (the
// published configuration until they write it), a revision, or, the zero
// Against, the published revision. The console reads its workspace; agents
// read the published configuration unless they plan (see PreviewInWorkspace).
type Against struct {
	Workspace bool
	Revision  *string
}

// draftOf is against as a draft to build; nil is the published revision.
func (r *Service) draftOf(ctx context.Context, against Against) (*Draft, error) {
	switch {
	case against.Revision != nil:
		return &Draft{Base: *against.Revision}, nil
	case against.Workspace && r.Plans != nil: // without a store, no workspace: the published one
		who, err := auth.Require(ctx, auth.PermRead)
		if err != nil {
			return nil, err
		}
		d, _, err := r.workspaceDraft(ctx, who.Username)
		return d, err
	}
	return nil, nil
}

// planContextOf is planContext for against.
func (r *Service) planContextOf(ctx context.Context, against Against) (planContext, error) {
	d, err := r.draftOf(ctx, against)
	if err != nil {
		return planContext{}, err
	}
	return r.planContext(ctx, d)
}
