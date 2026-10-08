package graph

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/configedit"
	"github.com/nethinwei/sql-mcp-server/x/revisionops"
)

func parseID(id string) (int64, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid revision id %q", id)
	}
	return n, nil
}

// ops returns the revision operations shared with the `store` CLI.
func (r *Resolver) ops() revisionops.Service {
	return revisionops.Service{Store: r.Store, Via: "admin-api"}
}

func (r *Resolver) revisionByID(ctx context.Context, id string) (revision.Revision, error) {
	n, err := parseID(id)
	if err != nil {
		return revision.Revision{}, err
	}
	return r.Store.Get(ctx, n)
}

// draft is a merged, validated and normalized configuration.
type draft struct {
	base    revision.Revision
	cfg     *config.Config
	payload []byte
}

// buildDraft applies in to its base revision through configedit, the same
// chain as `store import`: decode, defaults, validation, store secret rule and
// the deterministic encoding.
func (r *Resolver) buildDraft(ctx context.Context, in DraftInput) (draft, error) {
	base, err := r.revisionByID(ctx, in.Base)
	if err != nil {
		return draft{}, err
	}
	if err := base.Verify(); err != nil {
		return draft{}, fmt.Errorf("base revision %d: %w", base.ID, err)
	}
	edit, err := toEdit(in)
	if err != nil {
		return draft{}, err
	}
	cfg, payload, err := configedit.Apply(base.Payload, edit)
	if err != nil {
		return draft{}, err
	}
	return draft{base: base, cfg: cfg, payload: payload}, nil
}

// restartChanges compares a configuration with the published one.
func (r *Resolver) restartChanges(ctx context.Context, next *config.Config) ([]string, error) {
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
