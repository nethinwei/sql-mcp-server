package graph

import (
	"context"
	"slices"
	"sort"
	"strconv"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
)

// tableKey identifies a table within a datasource: tables of different
// schemas may share a name.
func tableKey(schema, table string) string {
	return schema + "." + table
}

// configuredTables maps each catalog table (by tableKey) to the configured
// entity of datasource that reads it, resolved by the catalog like the runtime
// does.
func configuredTables(datasource string, cat introspect.Catalog, cfg *config.Config) map[string]config.EntityConfig {
	out := map[string]config.EntityConfig{}
	for _, e := range cfg.Entities {
		if e.DatasourceName() != datasource || e.Kind == "procedure" {
			continue
		}
		if d, ok := cat.Lookup(e.Schema, e.PhysicalSource()); ok {
			out[tableKey(d.Schema, d.Source)] = e
		}
	}
	return out
}

// buildSchemaImport compares the scanned tables with the configured entities
// of datasource and proposes a zero-permission candidate entity per table.
func buildSchemaImport(datasource string, cat introspect.Catalog, cfg *config.Config) *SchemaImport {
	configured := configuredTables(datasource, cat, cfg)
	taken := map[string]bool{}
	for _, e := range cfg.Entities {
		taken[e.Name] = true
	}
	candidates := candidateNames(datasource, cat.Tables, configured, taken)
	out := &SchemaImport{
		Datasource: datasource, DefaultSchema: optional(cat.Default), Tables: make([]ImportTable, 0, len(cat.Tables)),
	}
	for _, d := range cat.Tables {
		table := ImportTable{
			Schema: d.Schema, Table: d.Source, Description: d.Description, Status: ImportStatusNew,
			Columns: importColumns(d),
		}
		if e, ok := configured[tableKey(d.Schema, d.Source)]; ok {
			name := e.Name
			table.Status, table.ConfiguredAs = ImportStatusConfigured, &name
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

// candidateNames maps each table (by tableKey) to the entity name a candidate
// would use: the configured name when the table is configured, otherwise the
// table name, qualified by schema when several schemas have that table and by
// datasource when another entity already uses the name.
func candidateNames(
	datasource string,
	discovered []entity.Entity,
	configured map[string]config.EntityConfig,
	taken map[string]bool,
) map[string]string {
	out := make(map[string]string, len(discovered))
	used := map[string]bool{}
	for name := range taken {
		used[name] = true
	}
	perName := map[string]int{}
	for _, d := range discovered {
		perName[d.Source]++
		if e, ok := configured[tableKey(d.Schema, d.Source)]; ok {
			out[tableKey(d.Schema, d.Source)] = e.Name
		}
	}
	for _, d := range discovered {
		key := tableKey(d.Schema, d.Source)
		if _, ok := out[key]; ok {
			continue
		}
		name := d.Source
		if perName[d.Source] > 1 && d.Schema != "" {
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

// candidateEntity proposes an entity with every column, the primary key and a
// belongs-to relationship per single-column foreign key. Table and column
// comments are not copied: at runtime they are the default descriptions, so
// later comment changes still apply and only a written description overrides.
// It carries no roles, grants or row policies: nobody can access it until an
// administrator grants access explicitly.
func candidateEntity(datasource string, d entity.Entity, names map[string]string) Entity {
	out := Entity{
		Name: names[tableKey(d.Schema, d.Source)], Source: optional(d.Source), Datasource: optional(datasource),
		Schema:     optional(d.Schema),
		PrimaryKey: orEmpty(d.PrimaryKey()), Params: []string{},
		Fields: make([]Field, 0, len(d.Attributes)), Relationships: []Relationship{},
		Mcp: &EntityMcp{DmlTools: true},
	}
	for _, a := range d.Attributes {
		out.Fields = append(out.Fields, Field{Name: a.Name})
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

// importCatalog scans schemas, or every schema of the database when none are
// named (only the default one when the introspector cannot list them).
func importCatalog(ctx context.Context, in introspect.Introspector, schemas []string) (introspect.Catalog, error) {
	if lister, ok := in.(introspect.SchemaLister); ok && len(schemas) == 0 {
		all, _, err := lister.Schemas(ctx)
		if err != nil {
			return introspect.Catalog{}, err
		}
		schemas = all
	}
	return introspect.LoadCatalog(ctx, in, schemas, len(schemas) == 0)
}

// referencedCatalog scans the schemas refs name, and the default schema when
// a ref names none.
func referencedCatalog(ctx context.Context, in introspect.Introspector, refs []TableRef) (introspect.Catalog, error) {
	var schemas []string
	unqualified := false
	for _, ref := range refs {
		if schema := deref(ref.Schema); schema == "" {
			unqualified = true
		} else if !slices.Contains(schemas, schema) {
			schemas = append(schemas, schema)
		}
	}
	return introspect.LoadCatalog(ctx, in, schemas, unqualified)
}

// lookupTableComments resolves each table's comments with one catalog per
// datasource; entries whose table is not found are nil.
func lookupTableComments(
	tables []TableRef,
	catalog func(datasource string, refs []TableRef) (introspect.Catalog, error),
) ([]*TableComments, error) {
	byDatasource := map[string][]TableRef{}
	for _, ref := range tables {
		byDatasource[ref.Datasource] = append(byDatasource[ref.Datasource], ref)
	}
	catalogs := map[string]introspect.Catalog{}
	for datasource, refs := range byDatasource {
		cat, err := catalog(datasource, refs)
		if err != nil {
			return nil, err
		}
		catalogs[datasource] = cat
	}
	out := make([]*TableComments, len(tables))
	for i, ref := range tables {
		if d, ok := catalogs[ref.Datasource].Lookup(deref(ref.Schema), ref.Table); ok {
			out[i] = tableComments(d)
		}
	}
	return out, nil
}

func tableComments(d entity.Entity) *TableComments {
	out := &TableComments{Description: d.Description, Columns: make([]ColumnComment, 0, len(d.Attributes))}
	for _, a := range d.Attributes {
		out.Columns = append(out.Columns, ColumnComment{Name: a.Name, Description: a.Description})
	}
	return out
}
