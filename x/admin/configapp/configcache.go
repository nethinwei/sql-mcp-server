package configapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strconv"
	"sync"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
)

// configsKept bounds the configurations SelectConfig keeps parsed.
const configsKept = 8

// configs keeps recently built workspace configurations, by their base and
// the hash of their edit: building a workspace onto a large revision takes
// a large part of a second, and the console asks for the same configuration
// on every page (a revision's own is kept by RevisionSpec). The
// configurations kept are shared: SelectConfig's callers only read them.
type configs struct {
	mu  sync.Mutex
	all []keptConfig // most recently used last
}

type keptConfig struct {
	key string
	cfg *config.Config
}

func (c *configs) get(key string) (*config.Config, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	i := slices.IndexFunc(c.all, func(k keptConfig) bool { return k.key == key })
	if i < 0 {
		return nil, false
	}
	k := c.all[i]
	c.all = append(slices.Delete(c.all, i, i+1), k)
	return k.cfg, true
}

func (c *configs) put(key string, cfg *config.Config) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.all = slices.DeleteFunc(c.all, func(k keptConfig) bool { return k.key == key })
	c.all = append(c.all, keptConfig{key: key, cfg: cfg})
	if len(c.all) > configsKept {
		c.all = c.all[len(c.all)-configsKept:]
	}
}

// keep returns the configuration kept under key, or builds and keeps it.
func (c *configs) keep(key string, build func() (*config.Config, error)) (*config.Config, error) {
	if cfg, ok := c.get(key); ok {
		return cfg, nil
	}
	cfg, err := build()
	if err != nil {
		return nil, err
	}
	c.put(key, cfg)
	return cfg, nil
}

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
