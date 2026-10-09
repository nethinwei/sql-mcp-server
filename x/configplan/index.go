package configplan

import (
	"github.com/nethinwei/sql-mcp-server/core/config"
)

// Index is every scanned table with where it stands, worked out once for a
// set of scans and a configuration: many scopes (catalog pages, counts of
// conditions, several administrators) filter it instead of each resolving
// the configured entities of every table again.
type Index struct {
	rows []Row
	// referenced counts the foreign keys of scanned tables referencing each
	// table, by Ref.
	referenced map[string]int
}

// NewIndex indexes the tables of sources against base and published (nil:
// none).
func NewIndex(sources []Source, base, published *config.Config) *Index {
	x := &Index{rows: Rows(sources, base, published, Scope{}), referenced: map[string]int{}}
	for _, src := range sources {
		for _, t := range src.Catalog.Tables {
			for _, fk := range t.ForeignKeys {
				schema := fk.RefSchema
				if schema == "" {
					schema = t.Schema
				}
				x.referenced[Ref(src.Datasource, schema, fk.RefRelation)]++
			}
		}
	}
	return x
}

// Rows lists the rows in scope, as Rows does.
func (x *Index) Rows(scope Scope) []Row {
	var out []Row
	for _, i := range x.Match(scope) {
		out = append(out, x.rows[i])
	}
	return out
}

// Match lists the positions of the rows in scope, in order: a few bytes a
// row, to keep for paging through them with At.
func (x *Index) Match(scope Scope) []int32 {
	m := scope.compile()
	var out []int32
	for i, row := range x.rows {
		if (m.datasources == nil || m.datasources[row.Datasource]) && m.table(row.Datasource, row.Table) && m.row(row) {
			out = append(out, int32(i))
		}
	}
	return out
}

// At is the row at a position Match listed.
func (x *Index) At(i int32) Row { return x.rows[i] }

// Len is how many tables are indexed.
func (x *Index) Len() int { return len(x.rows) }

// Referenced counts the foreign keys of scanned tables referencing a table.
func (x *Index) Referenced(ref string) int { return x.referenced[ref] }
