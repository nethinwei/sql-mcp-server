package configplan

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
)

// Source is what was scanned of one datasource.
type Source struct {
	Datasource string
	// Catalog holds every scanned schema of the datasource.
	Catalog introspect.Catalog
	// Listed are the schemas the datasource lists; nil when unknown.
	Listed []string
	// Scanned are the schemas scanned.
	Scanned []string
	// ScannedAt is when each scanned schema was scanned.
	ScannedAt map[string]time.Time
}

// Row is a scanned table with where it stands against the configuration.
type Row struct {
	Ref        string
	Datasource string
	Table      entity.Entity
	Status     Status
	// Entity is the entity of the base configuration that reads the table.
	Entity *config.EntityConfig
	// Added are columns without a field, Missing fields without a column.
	Added, Missing []string
	// ScannedAt is when the table's schema was scanned; zero when unknown.
	ScannedAt time.Time
}

// Drift reports whether the entity's fields differ from the columns.
func (r Row) Drift() bool { return len(r.Added) > 0 || len(r.Missing) > 0 }

// Ref names a table: datasource.schema.table, or datasource.table in a
// datasource without schemas, like an entity ID.
func Ref(datasource, schema, table string) string { return entity.ID(datasource, schema, table) }

// Rows lists the tables of sources in scope, sorted by datasource, schema and
// table, with their status against base and published (nil: none). The
// scope is compiled once; conditions on the table alone are checked before
// its status is worked out.
func Rows(sources []Source, base, published *config.Config, scope Scope) []Row {
	m := scope.compile()
	var out []Row
	for _, src := range sources {
		if m.datasources != nil && !m.datasources[src.Datasource] {
			continue
		}
		inBase := ConfiguredTables(src.Datasource, src.Catalog, base)
		inPublished := map[string]config.EntityConfig{}
		if published != nil {
			inPublished = ConfiguredTables(src.Datasource, src.Catalog, published)
		}
		for _, t := range src.Catalog.Tables {
			if !m.table(src.Datasource, t) {
				continue
			}
			row := newRow(src.Datasource, t, inBase, inPublished)
			row.ScannedAt = src.ScannedAt[t.Schema]
			if m.row(row) {
				out = append(out, row)
			}
		}
	}
	slices.SortFunc(out, func(a, b Row) int {
		return cmp.Or(cmp.Compare(a.Datasource, b.Datasource), cmp.Compare(a.Table.Schema, b.Table.Schema),
			cmp.Compare(a.Table.Source, b.Table.Source))
	})
	return out
}

func newRow(datasource string, t entity.Entity, inBase, inPublished map[string]config.EntityConfig) Row {
	key := TableKey(t.Schema, t.Source)
	row := Row{Ref: Ref(datasource, t.Schema, t.Source), Datasource: datasource, Table: t, Status: StatusNew}
	e, ok := inBase[key]
	if !ok {
		return row
	}
	row.Entity, row.Status = &e, StatusWorkspace
	if _, ok := inPublished[key]; ok {
		row.Status = StatusPublished
	}
	row.Added, row.Missing = drift(e, t)
	return row
}

// drift lists the columns without a field and the fields without a column.
func drift(e config.EntityConfig, t entity.Entity) (added, missing []string) {
	declared := make(map[string]bool, len(e.Fields))
	for _, f := range e.Fields {
		declared[f.Name] = true
	}
	columns := make(map[string]bool, len(t.Attributes))
	for _, a := range t.Attributes {
		columns[a.Name] = true
		if !declared[a.Name] {
			added = append(added, a.Name)
		}
	}
	for _, f := range e.Fields {
		if !columns[f.Name] {
			missing = append(missing, f.Name)
		}
	}
	return added, missing
}

// matcher is a scope compiled for matching many tables: sets for lists,
// lower-cased patterns and search text.
type matcher struct {
	datasources, refs map[string]bool
	schemas, tables   globs
	search            string
	status            map[Status]bool
	drift             bool
}

func set[T comparable](xs []T) map[T]bool {
	if len(xs) == 0 {
		return nil
	}
	out := make(map[T]bool, len(xs))
	for _, x := range xs {
		out[x] = true
	}
	return out
}

func (s Scope) compile() matcher {
	return matcher{
		datasources: set(s.Datasources), refs: set(s.Refs), schemas: compileGlobs(s.Schemas),
		tables: compileGlobs(s.Tables), search: strings.ToLower(strings.TrimSpace(s.Search)), status: set(s.Status),
		drift: s.Drift,
	}
}

// table checks the conditions on the table itself.
func (m matcher) table(datasource string, t entity.Entity) bool {
	switch {
	case m.schemas.set() && !m.schemas.match(t.Schema),
		m.tables.set() && !m.tables.match(t.Source),
		m.refs != nil && !m.refs[Ref(datasource, t.Schema, t.Source)]:
		return false
	}
	return m.search == "" || strings.Contains(strings.ToLower(t.Source), m.search) ||
		strings.Contains(strings.ToLower(t.Description), m.search)
}

// row checks the conditions on where the table stands.
func (m matcher) row(r Row) bool {
	return (m.status == nil || m.status[r.Status]) && (!m.drift || r.Drift())
}

// Unscanned lists, per datasource in scope, the listed schemas the scope
// covers that were never scanned: the plan cannot see their tables.
func Unscanned(sources []Source, scope Scope) map[string][]string {
	out := map[string][]string{}
	for _, src := range sources {
		if len(scope.Datasources) > 0 && !slices.Contains(scope.Datasources, src.Datasource) {
			continue
		}
		scanned, schemas := set(src.Scanned), compileGlobs(scope.Schemas)
		for _, name := range src.Listed {
			if !scanned[name] && (!schemas.set() || schemas.match(name)) {
				out[src.Datasource] = append(out[src.Datasource], name)
			}
		}
	}
	return out
}
