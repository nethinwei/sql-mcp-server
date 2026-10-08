package mysql

import (
	"context"
	"database/sql"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
)

// Introspector discovers entities from information_schema. Exported so the
// oceanbase provider can reuse it (OceanBase is MySQL-protocol compatible).
type Introspector struct {
	db *sql.DB
}

// NewIntrospector returns an introspect.Introspector backed by db.
func NewIntrospector(db *sql.DB) introspect.Introspector {
	return Introspector{db: db}
}

// systemSchemas are the MySQL and OceanBase databases a schema import skips.
var systemSchemas = []string{
	"information_schema", "mysql", "performance_schema", "sys", "oceanbase", "LBACSYS", "ORAAUDITOR",
}

// Schemas implements introspect.SchemaLister: every database except the system
// ones, and the connection's current database.
func (i Introspector) Schemas(ctx context.Context) ([]string, string, error) {
	rows, err := i.db.QueryContext(ctx, `SELECT schema_name FROM information_schema.schemata ORDER BY schema_name`)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, "", err
		}
		if !contains(systemSchemas, name) {
			out = append(out, name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	current, err := i.currentDatabase(ctx)
	return out, current, err
}

func (i Introspector) currentDatabase(ctx context.Context) (string, error) {
	var current sql.NullString
	err := i.db.QueryRowContext(ctx, `SELECT DATABASE()`).Scan(&current)
	return current.String, err
}

// Discover implements introspect.Introspector. It lists base tables (filtered
// to the requested schemas in memory, by default the current database, where
// unqualified names resolve) with their columns and primary keys.
func (i Introspector) Discover(ctx context.Context, sources []string) ([]entity.Entity, error) {
	if len(sources) == 0 {
		current, err := i.currentDatabase(ctx)
		if err != nil || current == "" {
			return nil, err
		}
		sources = []string{current}
	}
	trows, err := i.db.QueryContext(ctx,
		`SELECT table_schema, table_name, COALESCE(table_comment, '') FROM information_schema.tables
		 WHERE table_type = 'BASE TABLE'`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = trows.Close() }()
	type tbl struct{ schema, name, comment string }
	var tables []tbl
	for trows.Next() {
		var t tbl
		if err := trows.Scan(&t.schema, &t.name, &t.comment); err != nil {
			return nil, err
		}
		if !contains(sources, t.schema) || config.IsStoreTable(t.name) {
			continue
		}
		tables = append(tables, t)
	}
	if err := trows.Err(); err != nil {
		return nil, err
	}
	entities := make([]entity.Entity, 0, len(tables))
	for _, t := range tables {
		attrs, keys, err := i.columns(ctx, t.schema, t.name)
		if err != nil {
			return nil, err
		}
		fks, err := i.foreignKeys(ctx, t.schema, t.name)
		if err != nil {
			return nil, err
		}
		entities = append(entities, entity.Entity{
			Name: t.name, Source: t.name, Schema: t.schema, Description: t.comment,
			Kind: entity.KindTable, Attributes: attrs, Keys: keys, ForeignKeys: fks,
		})
	}
	return entities, nil
}

func (i Introspector) columns(ctx context.Context, schema, table string) ([]entity.Attribute, []entity.Key, error) {
	crows, err := i.db.QueryContext(ctx,
		`SELECT column_name, data_type, is_nullable, COALESCE(column_comment, '')
		 FROM information_schema.columns
		 WHERE table_schema=? AND table_name=? ORDER BY ordinal_position`, schema, table)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = crows.Close() }()
	var attrs []entity.Attribute
	for crows.Next() {
		var name, dataType, nullable, comment string
		if err := crows.Scan(&name, &dataType, &nullable, &comment); err != nil {
			return nil, nil, err
		}
		attrs = append(attrs, entity.Attribute{
			Name: name, Description: comment,
			Domain: entity.Domain{Type: dataType, Nullable: strings.EqualFold(nullable, "YES")},
		})
	}
	if err := crows.Err(); err != nil {
		return nil, nil, err
	}
	pk, err := i.primaryKey(ctx, schema, table)
	if err != nil {
		return nil, nil, err
	}
	return attrs, pk, nil
}

func (i Introspector) primaryKey(ctx context.Context, schema, table string) ([]entity.Key, error) {
	krows, err := i.db.QueryContext(ctx,
		`SELECT kcu.column_name
		 FROM information_schema.table_constraints tc
		 JOIN information_schema.key_column_usage kcu
		   ON tc.constraint_name = kcu.constraint_name
		  AND tc.table_schema = kcu.table_schema
		  AND tc.table_name = kcu.table_name
		 WHERE tc.table_schema=? AND tc.table_name=? AND tc.constraint_type='PRIMARY KEY'
		 ORDER BY kcu.ordinal_position`, schema, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = krows.Close() }()
	var cols []string
	for krows.Next() {
		var col string
		if err := krows.Scan(&col); err != nil {
			return nil, err
		}
		cols = append(cols, col)
	}
	if err := krows.Err(); err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return nil, nil
	}
	return []entity.Key{{Name: "pk", Columns: cols, Primary: true}}, nil
}

// foreignKeys lists foreign keys with columns in key order.
func (i Introspector) foreignKeys(ctx context.Context, schema, table string) ([]entity.ForeignKey, error) {
	rows, err := i.db.QueryContext(ctx,
		`SELECT constraint_name, column_name, referenced_table_schema, referenced_table_name, referenced_column_name
		 FROM information_schema.key_column_usage
		 WHERE table_schema=? AND table_name=? AND referenced_table_name IS NOT NULL
		 ORDER BY constraint_name, ordinal_position`, schema, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var fks []entity.ForeignKey
	for rows.Next() {
		var name, col, refSchema, refTable, refCol string
		if err := rows.Scan(&name, &col, &refSchema, &refTable, &refCol); err != nil {
			return nil, err
		}
		if n := len(fks); n == 0 || fks[n-1].Name != name {
			fks = append(fks, entity.ForeignKey{Name: name, RefSchema: refSchema, RefRelation: refTable})
		}
		last := &fks[len(fks)-1]
		last.Columns = append(last.Columns, col)
		last.RefColumns = append(last.RefColumns, refCol)
	}
	return fks, rows.Err()
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
