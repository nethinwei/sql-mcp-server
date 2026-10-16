package configplan

import (
	"slices"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
)

// Gone is a configured table or view whose table no longer exists.
type Gone struct {
	Datasource string
	// Ref names the table (see Ref); Entity is the entity's ID.
	Ref, Entity string
	// Scanned is true when a scan of its schema missed it, false when only
	// the serving configuration found it missing.
	Scanned bool
}

// GoneTables lists, by entity ID, the configured tables and views of base in
// scope whose table is gone: its schema was scanned without it, or missing
// names it (the serving configuration found its table missing). A table of
// a schema never scanned is not known to be gone. Only the conditions on the
// table apply: a gone table has no status and no fields to differ.
func GoneTables(sources []Source, base *config.Config, scope Scope, missing map[string]bool) []Gone {
	m := scope.compile()
	byDatasource := map[string]Source{}
	for _, src := range sources {
		byDatasource[src.Datasource] = src
	}
	var out []Gone
	for _, e := range base.Entities {
		ds := e.DatasourceName()
		if e.Kind == "procedure" || m.datasources != nil && !m.datasources[ds] {
			continue
		}
		table := entity.Entity{Schema: e.Schema, Source: e.PhysicalSource(), Description: e.Description}
		src, ok := byDatasource[ds]
		if table.Schema == "" && ok {
			table.Schema = src.Catalog.Default
		}
		if !m.table(ds, table) {
			continue
		}
		g := Gone{Datasource: ds, Ref: Ref(ds, table.Schema, table.Source), Entity: e.ID()}
		if ok && table.Schema != "" && scannedSchema(src, table.Schema) {
			_, found := src.Catalog.Lookup(e.Schema, e.PhysicalSource())
			g.Scanned = !found
		}
		if g.Scanned || missing[g.Entity] {
			out = append(out, g)
		}
	}
	slices.SortFunc(out, func(a, b Gone) int { return strings.Compare(a.Entity, b.Entity) })
	return out
}

func scannedSchema(src Source, schema string) bool {
	return slices.ContainsFunc(src.Scanned, func(s string) bool {
		return s == schema || src.Catalog.FoldCase && strings.EqualFold(s, schema)
	})
}

// removeGone removes the entities of gone tables, but those excluded; the
// references naming them go with them (see qualifyReferences).
func (ev *evaluation) removeGone(gone []Gone, exclude map[string]bool) {
	for _, g := range gone {
		i, ok := ev.index[g.Entity]
		if !ok || exclude[g.Ref] {
			continue
		}
		ev.cfg.Entities = slices.Delete(ev.cfg.Entities, i, i+1)
		for id, at := range ev.index {
			if at > i {
				ev.index[id] = at - 1
			}
		}
		delete(ev.index, g.Entity)
		delete(ev.changed, g.Entity)
		ev.removed = append(ev.removed, g.Entity)
		ev.summarize(Change{Kind: ChangeRemoveTable}, g.Entity, 0)
	}
}
