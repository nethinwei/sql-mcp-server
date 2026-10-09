package tool

import (
	"maps"
	"slices"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/cache"
	"github.com/nethinwei/sql-mcp-server/core/entity"
)

// CacheTarget names the cached reads a write invalidates: Relation of the
// physical database Physical, or all of it when Relation is empty (see
// cache.Cache).
type CacheTarget struct {
	Physical string
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

// WriteTargets is what a write through each entity invalidates, worked out
// once per configuration (see BuildWriteTargets), and which datasources reach
// each physical database, whose session reads then follow the write.
type WriteTargets struct {
	byEntity    map[string][]CacheTarget
	datasources map[string][]string // by physical database
	physical    map[string]string   // by datasource
}

// BuildWriteTargets works out what a write through each entity of reg
// invalidates, so writes do not walk the registry.
func BuildWriteTargets(reg *entity.Registry) *WriteTargets {
	w := &WriteTargets{byEntity: map[string][]CacheTarget{}, datasources: map[string][]string{},
		physical: map[string]string{}}
	if reg == nil {
		return w
	}
	reaching := map[string]map[string]bool{}
	add := func(database, datasource string) {
		if reaching[database] == nil {
			reaching[database] = map[string]bool{}
		}
		reaching[database][datasource] = true
	}
	for _, e := range reg.Entities() {
		if e.Kind != entity.KindProcedure {
			w.physical[e.DatasourceName()] = physicalDatabase(e)
			add(physicalDatabase(e), e.DatasourceName())
		}
	}
	for _, e := range reg.Entities() { // datasources serving only procedures
		if _, ok := w.physical[e.DatasourceName()]; !ok {
			w.physical[e.DatasourceName()] = w.database(e.DatasourceName())
			add(w.physical[e.DatasourceName()], e.DatasourceName())
		}
	}
	for database, datasources := range reaching {
		w.datasources[database] = slices.Sorted(maps.Keys(datasources))
	}
	for _, e := range reg.Entities() {
		w.byEntity[e.Name] = w.compute(reg, e)
	}
	return w
}

// compute lists what a write through e may change: its relation and, through
// any number of cascading foreign keys, the relations they rewrite. A derived
// entity (an updatable view) writes some base table, a relation with triggers
// or rules anything in its database, and a procedure the entities it declares
// it affects, otherwise anything in its database. When a cascade reaches a
// relation no entity exposes, what it cascades to further is unknown, so the
// whole database is invalidated.
func (w *WriteTargets) compute(reg *entity.Registry, e entity.Entity) []CacheTarget {
	whole := func(e entity.Entity) []CacheTarget {
		if e.Kind == entity.KindProcedure {
			return []CacheTarget{{Physical: w.database(e.DatasourceName())}}
		}
		return []CacheTarget{{Physical: physicalDatabase(e)}}
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
		e := writes[0]
		writes = writes[1:]
		if seen[e.Name] {
			continue
		}
		seen[e.Name] = true
		if e.SideEffects { // triggers or rules may write anything in the database
			return whole(e)
		}
		targets = append(targets, CacheTarget{Physical: physicalDatabase(e), Relation: cacheRelation(e)})
		for _, c := range e.Cascades {
			child, ok := reg.Resolve(c.Entity)
			if !ok {
				return whole(e)
			}
			writes = append(writes, child.Entity)
		}
	}
	return compactTargets(targets)
}

// database is the physical database of a datasource.
func (w *WriteTargets) database(datasource string) string {
	if database, ok := w.physical[datasource]; ok {
		return database
	}
	return physicalDatabase(entity.Entity{DataSource: datasource})
}

// compactTargets drops repeated targets and those a whole-database target of
// the same database covers, keeping the order.
func compactTargets(targets []CacheTarget) []CacheTarget {
	whole := map[string]bool{}
	for _, t := range targets {
		if t.Relation == "" {
			whole[t.Physical] = true
		}
	}
	seen := map[CacheTarget]bool{}
	out := targets[:0:0]
	for _, t := range targets {
		if whole[t.Physical] {
			t.Relation = ""
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// writeTargets is the precomputed targets of tc, or those of its registry.
func writeTargets(tc Context) *WriteTargets {
	if tc.WriteTargets != nil {
		return tc.WriteTargets
	}
	return BuildWriteTargets(tc.Registry)
}

// physicalDatabase is the server and catalog part of an entity's relation key
// (see introspect.Physical.RelationKey); without a physical identity, it is
// the datasource's.
func physicalDatabase(e entity.Entity) string {
	parts := strings.SplitN(e.RelationKey(), "\x00", 3)
	return parts[0] + "\x00" + parts[1]
}

// afterWrite applies a write through e outside a transaction, or marks what it
// changed for the commit of one.
func afterWrite(tc Context, e entity.Entity, transaction string) error {
	w := writeTargets(tc)
	targets, ok := w.byEntity[e.Name]
	if !ok {
		targets = w.compute(tc.Registry, e)
	}
	if transaction == "" {
		return applyWrite(tc, w, targets)
	}
	if tc.Transactions == nil {
		return ErrTransactionNotFound
	}
	return tc.Transactions.MarkDirty(transaction, tc.Session, tc.Role, tc.Subject, e.DatasourceName(),
		txConnections(tc, e.DatasourceName()), targets...)
}

// applyWrite follows a committed write: the session's reads through every
// datasource reaching a written database follow it, and the cached reads it
// changed are dropped.
func applyWrite(tc Context, w *WriteTargets, targets []CacheTarget) error {
	recorded := map[string]bool{}
	for _, t := range targets {
		if !recorded[t.Physical] {
			recorded[t.Physical] = true
			for _, datasource := range w.datasources[t.Physical] {
				tc.Writes.Record(tc.Session, datasource)
			}
		}
	}
	return invalidate(tc.Cache, targets)
}

func invalidate(c cache.Cache[[]map[string]any], targets []CacheTarget) error {
	if c == nil {
		return nil
	}
	for _, t := range targets {
		if err := c.Invalidate(t.Physical, t.Relation); err != nil {
			return err
		}
	}
	return nil
}
