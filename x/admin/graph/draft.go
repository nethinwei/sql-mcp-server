package graph

import (
	"context"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/admin/configapp"
	"github.com/nethinwei/sql-mcp-server/x/revisionops"
)

// ops returns the revision operations shared with the `store` CLI.
func (r *Resolver) ops() revisionops.Service { return r.App.Ops() }

func (r *Resolver) revisionByID(ctx context.Context, id string) (revision.Revision, error) {
	return r.App.Revision(ctx, id)
}

// draft is a merged, validated and normalized configuration.
type draft struct {
	base    revision.Revision
	cfg     *config.Config
	payload []byte
}

// toDraft is the configuration service's draft of a draft input.
func toDraft(in *DraftInput) (*configapp.Draft, error) {
	if in == nil {
		return nil, nil
	}
	edit, err := toEdit(*in)
	if err != nil {
		return nil, err
	}
	return &configapp.Draft{Base: in.Base, Edit: edit}, nil
}

// buildDraft applies in to its base revision; see configapp.BuildDraft.
func (r *Resolver) buildDraft(ctx context.Context, in DraftInput) (draft, error) {
	d, err := toDraft(&in)
	if err != nil {
		return draft{}, err
	}
	built, err := r.App.BuildDraft(ctx, *d)
	if err != nil {
		return draft{}, err
	}
	return draft{base: built.Base, cfg: built.Config, payload: built.Payload}, nil
}
