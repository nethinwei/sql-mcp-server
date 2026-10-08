package graph

import (
	"slices"
	"sort"
	"strconv"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
)

// tableKey identifies a table within a datasource: tables of different
// schemas may share a name.
func tableKey(schema, table string) string {
	return schema + "." + table
}

// configuredTables matches configured entities of one datasource to tables.
// An entity naming its schema matches that table only; one without a schema
// matches by name, and only when exactly one discovered table has that name.
type configuredTables struct {
	scoped   map[string]config.EntityConfig // by tableKey
	unscoped map[string]config.EntityConfig // by table name
	tables   map[string]int                 // discovered tables per name
}

func newConfiguredTables(datasource string, discovered []entity.Entity, cfg *config.Config) configuredTables {
	c := configuredTables{
		scoped: map[string]config.EntityConfig{}, unscoped: map[string]config.EntityConfig{}, tables: map[string]int{},
	}
	for _, d := range discovered {
		c.tables[d.Source]++
	}
	for _, e := range cfg.Entities {
		if entityDatasource(e) != datasource {
			continue
		}
		if e.Schema != "" {
			c.scoped[tableKey(e.Schema, entitySource(e))] = e
		} else {
			c.unscoped[entitySource(e)] = e
		}
	}
	return c
}

func (c configuredTables) lookup(d entity.Entity) (config.EntityConfig, bool) {
	if e, ok := c.scoped[tableKey(d.Schema, d.Source)]; ok {
		return e, true
	}
	e, ok := c.unscoped[d.Source]
	return e, ok && c.tables[d.Source] == 1
}

// buildSchemaImport compares discovered tables with the configured entities
// of datasource and proposes a zero-permission candidate entity per table.
func buildSchemaImport(datasource string, discovered []entity.Entity, cfg *config.Config) *SchemaImport {
	configured := newConfiguredTables(datasource, discovered, cfg)
	taken := map[string]bool{}
	for _, e := range cfg.Entities {
		taken[e.Name] = true
	}
	candidates := candidateNames(datasource, discovered, configured, taken)
	out := &SchemaImport{Datasource: datasource, Tables: make([]ImportTable, 0, len(discovered))}
	for _, d := range discovered {
		table := ImportTable{
			Schema: d.Schema, Table: d.Source, Description: d.Description, Status: ImportStatusNew,
			AddedColumns: []string{}, MissingColumns: []string{}, Columns: importColumns(d),
		}
		if e, ok := configured.lookup(d); ok {
			name := e.Name
			table.Status, table.ConfiguredAs = ImportStatusConfigured, &name
			table.AddedColumns, table.MissingColumns = columnDiff(d, e)
		}
		candidate := candidateEntity(datasource, d, candidates)
		table.Candidate = &candidate
		out.Tables = append(out.Tables, table)
	}
	addReverseRelationships(out.Tables)
	sort.Slice(out.Tables, func(i, j int) bool {
		a, b := out.Tables[i], out.Tables[j]
		return a.Schema < b.Schema || a.Schema == b.Schema && a.Table < b.Table
	})
	return out
}

func entityDatasource(e config.EntityConfig) string {
	if e.DataSource == "" {
		return "default"
	}
	return e.DataSource
}

func entitySource(e config.EntityConfig) string {
	if e.Source == "" {
		return e.Name
	}
	return e.Source
}

// candidateNames maps each table (by tableKey) to the entity name a candidate
// would use: the configured name when the table is configured, otherwise the
// table name, qualified by schema when several schemas have that table and by
// datasource when another entity already uses the name.
func candidateNames(
	datasource string,
	discovered []entity.Entity,
	configured configuredTables,
	taken map[string]bool,
) map[string]string {
	out := make(map[string]string, len(discovered))
	used := map[string]bool{}
	for name := range taken {
		used[name] = true
	}
	for _, d := range discovered {
		if e, ok := configured.lookup(d); ok {
			out[tableKey(d.Schema, d.Source)] = e.Name
		}
	}
	for _, d := range discovered {
		key := tableKey(d.Schema, d.Source)
		if _, ok := out[key]; ok {
			continue
		}
		name := d.Source
		if configured.tables[d.Source] > 1 && d.Schema != "" {
			name = d.Schema + "_" + d.Source
		}
		if used[name] {
			name = datasource + "_" + name
		}
		base := name
		for n := 2; used[name]; n++ {
			name = base + "_" + strconv.Itoa(n)
		}
		used[name] = true
		out[key] = name
	}
	return out
}

func importColumns(d entity.Entity) []ImportColumn {
	pk := d.PrimaryKey()
	out := make([]ImportColumn, 0, len(d.Attributes))
	for _, a := range d.Attributes {
		out = append(out, ImportColumn{
			Name: a.Name, Type: a.Domain.Type, Nullable: a.Domain.Nullable,
			Description: a.Description, PrimaryKey: slices.Contains(pk, a.Name),
		})
	}
	return out
}

func columnDiff(d entity.Entity, e config.EntityConfig) (added, missing []string) {
	declared := map[string]bool{}
	for _, f := range e.Fields {
		declared[f.Name] = true
	}
	present := map[string]bool{}
	for _, a := range d.Attributes {
		present[a.Name] = true
		if !declared[a.Name] {
			added = append(added, a.Name)
		}
	}
	for _, f := range e.Fields {
		if !present[f.Name] {
			missing = append(missing, f.Name)
		}
	}
	return orEmpty(added), orEmpty(missing)
}

// candidateEntity proposes an entity with every column, the primary key, the
// table comment and a belongs-to relationship per single-column foreign key.
// It carries no roles, grants or row policies: nobody can access it until an
// administrator grants access explicitly.
func candidateEntity(datasource string, d entity.Entity, names map[string]string) Entity {
	out := Entity{
		Name: names[tableKey(d.Schema, d.Source)], Source: d.Source, Datasource: datasource,
		Schema: optional(d.Schema), Kind: "table",
		Description: optional(d.Description), PrimaryKey: orEmpty(d.PrimaryKey()), Params: []string{},
		Fields: make([]Field, 0, len(d.Attributes)), Relationships: []Relationship{},
		Mcp: &EntityMcp{DmlTools: true},
	}
	for _, a := range d.Attributes {
		out.Fields = append(out.Fields, Field{Name: a.Name, Description: optional(a.Description)})
	}
	for _, fk := range d.ForeignKeys {
		refSchema := fk.RefSchema
		if refSchema == "" {
			refSchema = d.Schema
		}
		target, ok := names[tableKey(refSchema, fk.RefRelation)]
		if !ok || len(fk.Columns) != 1 {
			continue
		}
		out.Relationships = append(out.Relationships, Relationship{
			Name: uniqueRelationshipName(out.Relationships, target), Target: target, Cardinality: "belongs-to",
			JoinOn: map[string]any{fk.Columns[0]: fk.RefColumns[0]},
		})
	}
	return out
}

// addReverseRelationships adds a has-many relationship on each parent for
// every belongs-to relationship pointing at it.
func addReverseRelationships(tables []ImportTable) {
	byName := make(map[string]*Entity, len(tables))
	for i := range tables {
		byName[tables[i].Candidate.Name] = tables[i].Candidate
	}
	for i := range tables {
		child := tables[i].Candidate
		for _, rel := range child.Relationships {
			if rel.Cardinality != "belongs-to" {
				continue
			}
			parent, ok := byName[rel.Target]
			if !ok {
				continue
			}
			joinOn := map[string]any{}
			for local, remote := range rel.JoinOn.(map[string]any) {
				joinOn[remote.(string)] = local
			}
			parent.Relationships = append(parent.Relationships, Relationship{
				Name: uniqueRelationshipName(parent.Relationships, child.Name), Target: child.Name,
				Cardinality: "has-many", JoinOn: joinOn,
			})
		}
	}
}

func uniqueRelationshipName(existing []Relationship, base string) string {
	name := base
	for n := 2; ; n++ {
		taken := false
		for _, r := range existing {
			if r.Name == name {
				taken = true
				break
			}
		}
		if !taken {
			return name
		}
		name = base + "_" + strconv.Itoa(n)
	}
}
