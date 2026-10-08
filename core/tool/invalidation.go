package tool

import (
	"maps"
	"slices"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/cache"
	"github.com/nethinwei/sql-mcp-server/core/entity"
)

// CacheTarget names the cached reads a write invalidates: Relation of
// Database, or all of Database when Relation is empty (see cache.Cache).
type CacheTarget struct {
	Database string
	Relation string
}

// cacheRelation is the cache relation of an entity's reads: empty for data
// derived from other relations, which any write to the database may change.
func cacheRelation(e entity.Entity) string {
	if e.Derived {
		return ""
	}
	return e.RelationKey()
}

// writeTargets lists what a write through e may change: its relation and,
// through any number of cascading foreign keys, the relations they rewrite. A
// derived entity (an updatable view) writes some base table, a relation with
// triggers or rules anything in its database, and a procedure the entities it
// declares it affects, otherwise anything in its database. When a cascade
// reaches a relation no entity exposes, what it cascades to further is
// unknown, so the whole database is invalidated. Each target is repeated for
// every datasource reaching the same database (see peerTargets).
func writeTargets(reg *entity.Registry, e entity.Entity) []CacheTarget {
	whole := func(w entity.Entity) []CacheTarget {
		return peerTargets(reg, []CacheTarget{{Database: w.DatasourceName()}})
	}
	writes := []entity.Entity{e}
	if e.Kind == entity.KindProcedure {
		if len(e.Affects) == 0 {
			return whole(e)
		}
		writes = writes[:0]
		for _, name := range e.Affects {
			affected, ok := reg.Resolve(name)
			if !ok {
				return whole(e)
			}
			writes = append(writes, affected.Entity)
		}
	}
	var targets []CacheTarget
	seen := map[string]bool{}
	for len(writes) > 0 {
		w := writes[0]
		writes = writes[1:]
		if seen[w.Name] {
			continue
		}
		seen[w.Name] = true
		if w.SideEffects { // triggers or rules may write anything in the database
			return whole(w)
		}
		targets = append(targets, CacheTarget{Database: w.DatasourceName(), Relation: cacheRelation(w)})
		for _, c := range w.Cascades {
			child, ok := reg.Resolve(c.Entity)
			if !ok {
				return whole(w)
			}
			writes = append(writes, child.Entity)
		}
	}
	return peerTargets(reg, targets)
}

// peerTargets repeats each target for every other datasource reaching the
// same physical database: their views may read the written relation, and
// their cached reads are keyed by their own datasource name.
func peerTargets(reg *entity.Registry, targets []CacheTarget) []CacheTarget {
	if reg == nil {
		return targets
	}
	databases := map[string]string{} // datasource → physical database
	for _, e := range reg.Entities() {
		if e.Kind != entity.KindProcedure {
			databases[e.DatasourceName()] = physicalDatabase(e)
		}
	}
	out := slices.Clone(targets)
	for _, t := range targets {
		database, known := databases[t.Database]
		for _, peer := range slices.Sorted(maps.Keys(databases)) {
			if known && peer != t.Database && databases[peer] == database {
				out = append(out, CacheTarget{Database: peer, Relation: t.Relation})
			}
		}
	}
	return out
}

// physicalDatabase is the server and catalog part of an entity's relation key
// (see introspect.Physical.RelationKey).
func physicalDatabase(e entity.Entity) string {
	parts := strings.SplitN(e.RelationKey(), "\x00", 3)
	return parts[0] + "\x00" + parts[1]
}

func afterWrite(tc Context, e entity.Entity, transaction string) error {
	targets := writeTargets(tc.Registry, e)
	if transaction == "" {
		tc.Writes.Record(tc.Session, e.DatasourceName())
		return invalidate(tc.Cache, targets)
	}
	if tc.Transactions == nil {
		return ErrTransactionNotFound
	}
	return tc.Transactions.MarkDirty(transaction, tc.Session, tc.Role, tc.Subject, e.DatasourceName(), targets...)
}

func invalidate(c cache.Cache[[]map[string]any], targets []CacheTarget) error {
	if c == nil {
		return nil
	}
	for _, t := range targets {
		if err := c.Invalidate(t.Database, t.Relation); err != nil {
			return err
		}
	}
	return nil
}
