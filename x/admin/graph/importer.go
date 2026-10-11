package graph

import (
	"context"
	"slices"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/internal/kit"
	"github.com/nethinwei/sql-mcp-server/x/admin/configapp"
	"github.com/nethinwei/sql-mcp-server/x/configplan"
)

// toSchemaImport shows the scanned tables of a datasource with the entity
// proposed for each.
func toSchemaImport(in *configapp.Imports) *SchemaImport {
	out := &SchemaImport{Datasource: in.Datasource, DefaultSchema: kit.Optional(in.DefaultSchema), Source: in.Source,
		Tables: make([]ImportTable, 0, len(in.Tables))}
	for _, c := range in.Tables {
		out.Tables = append(out.Tables, importTable(c))
	}
	return out
}

// importTable shows a scanned table with the entity proposed for it.
func importTable(c configplan.Candidate) ImportTable {
	d := c.Table
	candidate := toEntity(c.Entity)
	candidate.ID = c.ID
	table := ImportTable{
		Schema: d.Schema, Table: d.Source, Description: d.Description, Status: ImportStatusNew,
		Columns: importColumns(d), Keys: importKeys(d), Indexes: importIndexes(d),
		ForeignKeys: importForeignKeys(d), SideEffects: d.SideEffects, Candidate: &candidate,
	}
	if c.Configured != nil {
		table.Status, table.ConfiguredAs = ImportStatusConfigured, kit.Optional(c.Configured.Name)
	}
	return table
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

func importKeys(d entity.Entity) []ImportKey {
	out := make([]ImportKey, 0, len(d.Keys))
	for _, k := range d.Keys {
		out = append(
			out,
			ImportKey{Name: k.Name, Columns: k.Columns, Primary: k.Primary, Reason: kit.Optional(k.Reason)},
		)
	}
	return out
}

func importIndexes(d entity.Entity) []ImportIndex {
	out := make([]ImportIndex, 0, len(d.Indexes))
	for _, ix := range d.Indexes {
		out = append(out, ImportIndex{
			Name: ix.Name, Method: ix.Method, Parts: kit.OrEmpty(ix.Parts), Unique: ix.Unique, Primary: ix.Primary,
			Where: kit.Optional(ix.Where),
		})
	}
	return out
}

func importForeignKeys(d entity.Entity) []ImportForeignKey {
	out := make([]ImportForeignKey, 0, len(d.ForeignKeys))
	for _, fk := range d.ForeignKeys {
		out = append(out, ImportForeignKey{
			Name: fk.Name, Columns: fk.Columns, RefSchema: fk.RefSchema, RefTable: fk.RefRelation,
			RefColumns: fk.RefColumns, OnDelete: kit.Optional(fk.OnDelete), OnUpdate: kit.Optional(fk.OnUpdate),
		})
	}
	return out
}

// referencedCatalog scans the schemas refs name, and the default schema when
// a ref names none.
func referencedCatalog(ctx context.Context, in introspect.Introspector, refs []TableRef) (introspect.Catalog, error) {
	var schemas []string
	unqualified := false
	for _, ref := range refs {
		if schema := kit.Deref(ref.Schema); schema == "" {
			unqualified = true
		} else if !slices.Contains(schemas, schema) {
			schemas = append(schemas, schema)
		}
	}
	return configapp.LoadCatalog(ctx, in, schemas, unqualified)
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
		if d, ok := catalogs[ref.Datasource].Lookup(kit.Deref(ref.Schema), ref.Table); ok {
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
