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

// configs keeps recently selected configurations parsed. Parsing a large
// revision, and building a workspace onto one, takes a large part of a
// second; the console asks for the same configuration on every page. A
// revision never changes, so its configuration is kept by ID; a workspace
// by its base and the hash of its edit. The configurations kept are shared:
// SelectConfig's callers only read them.
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
// one when id is nil; the published one is looked up without its payload.
func (r *Service) revisionConfig(ctx context.Context, id *string) (*config.Config, error) {
	var key string
	if id != nil {
		key = "rev:" + *id
	} else {
		head, err := r.Store.PublishedSummary(ctx)
		if err != nil {
			return nil, err
		}
		key = "rev:" + strconv.FormatInt(head.ID, 10)
	}
	return r.configs.keep(key, func() (*config.Config, error) {
		var rev revision.Revision
		var err error
		if id != nil {
			rev, err = r.Revision(ctx, *id)
		} else {
			rev, err = r.Store.Published(ctx)
		}
		if err != nil {
			return nil, err
		}
		return bootstrap.LoadRevision(rev)
	})
}
