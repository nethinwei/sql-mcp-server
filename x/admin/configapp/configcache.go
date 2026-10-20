package configapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
)

// configsKept bounds the configurations kept parsed: built workspaces' (see
// BuildDraft) and revisions' (see RevisionSpec).
const configsKept = 8

// draftKey names a workspace's configuration: its base and its edit.
func draftKey(d Draft) (string, error) {
	edit, err := json.Marshal(d.Edit)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(edit)
	return "draft:" + d.Base + ":" + hex.EncodeToString(sum[:]), nil
}

// revisionConfig is the configuration of revision id, or of the published
// one when id is nil (looked up without its payload); see RevisionSpec.
func (r *Service) revisionConfig(ctx context.Context, id *string) (*config.Config, error) {
	var rev int64
	if id != nil {
		n, err := ParseID(*id)
		if err != nil {
			return nil, err
		}
		rev = n
	} else {
		head, err := r.Store.PublishedSummary(ctx)
		if err != nil {
			return nil, err
		}
		rev = head.ID
	}
	_, cfg, err := r.RevisionSpec(rev, func() (revision.Revision, error) { return r.Store.Get(ctx, rev) })
	return cfg, err
}

// keptSpec is a revision's configuration as written and in effect.
type keptSpec struct {
	spec *config.Spec
	cfg  *config.Config
	err  error
}

// RevisionSpec is revision id's configuration as written and in effect,
// parsed once per revision (a revision never changes; parsing a large one
// takes a large part of a second): load reads the revision when it is not
// kept, and a revision that could not be read is not kept. Both are shared:
// callers only read them.
func (r *Service) RevisionSpec(id int64, load func() (revision.Revision, error)) (*config.Spec, *config.Config, error) {
	r.specsOnce.Do(func() { r.specs = newLRU[keptSpec](configsKept) })
	var loadErr error
	k := r.specs.get(strconv.FormatInt(id, 10), func() (keptSpec, int) {
		rev, err := load()
		if err != nil {
			loadErr = err
			return keptSpec{err: err}, -1
		}
		spec, cfg, err := bootstrap.LoadRevisionSpec(rev)
		return keptSpec{spec: spec, cfg: cfg, err: err}, 1
	})
	if loadErr != nil {
		return nil, nil, loadErr
	}
	return k.spec, k.cfg, k.err
}
