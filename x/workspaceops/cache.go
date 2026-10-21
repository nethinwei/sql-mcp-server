package workspaceops

import (
	"errors"
	"slices"
	"sync"

	"github.com/nethinwei/sql-mcp-server/core/workspace"
)

// itemsKept bounds the revisions an ItemsCache keeps: the published one and
// the few older ones workspaces still build on.
const itemsKept = 4

// ItemsCache keeps the items of recently read revisions. Decoding a large
// revision takes most of a workspace operation, and every operation reads
// its base's items; a revision never changes, so they are kept by ID. The
// snapshots kept are shared: operations only read them (workspace.Snapshot
// and the merge rules build new values). The zero value is ready to use.
type ItemsCache struct {
	mu  sync.Mutex
	all []keptItems // most recently used last
	// building are the revisions decoding: operations asking for one at
	// once (consoles opening after a restart) decode it once.
	building map[int64]*itemsBuild
}

type itemsBuild struct {
	done  chan struct{}
	items workspace.Snapshot
	err   error
}

// errBuildEnded is the answer of a build that panicked.
var errBuildEnded = errors.New("workspace: reading the revision's items failed")

type keptItems struct {
	id    int64
	items workspace.Snapshot
	// published: the revision was published once (it stays so).
	published bool
}

// Items are revision id's items as kept, or as build makes them (then kept).
func (c *ItemsCache) Items(id int64, build func() (workspace.Snapshot, error)) (workspace.Snapshot, error) {
	if items, ok := c.get(id); ok {
		return items, nil
	}
	c.mu.Lock()
	if b, ok := c.building[id]; ok {
		c.mu.Unlock()
		<-b.done
		return b.items, b.err
	}
	b := &itemsBuild{done: make(chan struct{}), err: errBuildEnded}
	if c.building == nil {
		c.building = map[int64]*itemsBuild{}
	}
	c.building[id] = b
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.building, id)
		c.mu.Unlock()
		close(b.done)
	}()
	b.items, b.err = build()
	if b.err == nil {
		c.put(id, b.items)
	}
	return b.items, b.err
}

func (c *ItemsCache) get(id int64) (workspace.Snapshot, bool) {
	k, ok := c.kept(id)
	return k.items, ok
}

// publishedItems are the items of revision id kept as a published one's.
func (c *ItemsCache) publishedItems(id int64) (workspace.Snapshot, bool) {
	k, ok := c.kept(id)
	return k.items, ok && k.published
}

func (c *ItemsCache) kept(id int64) (keptItems, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	i := slices.IndexFunc(c.all, func(k keptItems) bool { return k.id == id })
	if i < 0 {
		return keptItems{}, false
	}
	k := c.all[i]
	c.all = append(slices.Delete(c.all, i, i+1), k)
	return k, true
}

func (c *ItemsCache) put(id int64, items workspace.Snapshot) {
	c.keep(keptItems{id: id, items: items})
}

func (c *ItemsCache) keep(kept keptItems) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.all = slices.DeleteFunc(c.all, func(k keptItems) bool { return k.id == kept.id })
	c.all = append(c.all, kept)
	if len(c.all) > itemsKept {
		c.all = c.all[len(c.all)-itemsKept:]
	}
}
