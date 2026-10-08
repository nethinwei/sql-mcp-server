package postgres

import (
	"context"
	"database/sql"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
)

// pgIntrospector discovers entities from information_schema.
type pgIntrospector struct {
	db *sql.DB
}

// Discover implements introspect.Introspector. It lists base tables in the
// given schemas (defaulting to "public") with their columns and primary keys.
func (i pgIntrospector) Discover(ctx context.Context, sources []string) ([]entity.Entity, error) {
	if len(sources) == 0 {
		sources = []string{"public"}
	}
	trows, err := i.db.QueryContext(ctx,
		`SELECT table_schema, table_name FROM information_schema.tables
		 WHERE table_type = 'BASE TABLE' AND table_schema = ANY($1)`, sources)
	if err != nil {
		return nil, err
	}
	defer func() { _ = trows.Close() }()
	type tbl struct{ schema, name string }
	var tables []tbl
	for trows.Next() {
		var t tbl
		if err := trows.Scan(&t.schema, &t.name); err != nil {
			return nil, err
		}
		if config.IsStoreTable(t.name) {
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
		comment, fks, err := i.tableMeta(ctx, t.schema, t.name)
		if err != nil {
			return nil, err
		}
		entities = append(entities, entity.Entity{
			Name:        t.name,
			Source:      t.name,
			Schema:      t.schema,
			Description: comment,
			Kind:        entity.KindTable,
			Attributes:  attrs,
			Keys:        keys,
			ForeignKeys: fks,
		})
	}
	return entities, nil
}

func (i pgIntrospector) columns(ctx context.Context, schema, table string) ([]entity.Attribute, []entity.Key, error) {
	crows, err := i.db.QueryContext(ctx,
		`SELECT column_name, data_type, is_nullable,
		        COALESCE(col_description(format('%I.%I', table_schema, table_name)::regclass,
		                                 ordinal_position::int), '')
		 FROM information_schema.columns
		 WHERE table_schema=$1 AND table_name=$2 ORDER BY ordinal_position`, schema, table)
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
			Name:        name,
			Description: comment,
			Domain: entity.Domain{
				Type:     dataType,
				Nullable: strings.EqualFold(nullable, "YES"),
			},
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

func (i pgIntrospector) primaryKey(ctx context.Context, schema, table string) ([]entity.Key, error) {
	krows, err := i.db.QueryContext(ctx,
		`SELECT kcu.column_name
		 FROM information_schema.table_constraints tc
		 JOIN information_schema.key_column_usage kcu
		   ON tc.constraint_name = kcu.constraint_name
		  AND tc.table_schema = kcu.table_schema
		 WHERE tc.table_schema=$1 AND tc.table_name=$2 AND tc.constraint_type='PRIMARY KEY'
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

// tableMeta returns the table comment and foreign keys (columns in key order).
func (i pgIntrospector) tableMeta(ctx context.Context, schema, table string) (string, []entity.ForeignKey, error) {
	var comment string
	if err := i.db.QueryRowContext(ctx,
		`SELECT COALESCE(obj_description(format('%I.%I', $1::text, $2::text)::regclass, 'pg_class'), '')`,
		schema, table).Scan(&comment); err != nil {
		return "", nil, err
	}
	rows, err := i.db.QueryContext(ctx,
		`SELECT con.conname, a.attname, rn.nspname, rc.relname, ra.attname
		 FROM pg_constraint con
		 CROSS JOIN LATERAL unnest(con.conkey, con.confkey) WITH ORDINALITY AS k(col, refcol, ord)
		 JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.col
		 JOIN pg_class rc ON rc.oid = con.confrelid
		 JOIN pg_namespace rn ON rn.oid = rc.relnamespace
		 JOIN pg_attribute ra ON ra.attrelid = con.confrelid AND ra.attnum = k.refcol
		 WHERE con.contype = 'f' AND con.conrelid = format('%I.%I', $1::text, $2::text)::regclass
		 ORDER BY con.conname, k.ord`, schema, table)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = rows.Close() }()
	fks, err := scanForeignKeys(rows)
	return comment, fks, err
}

// scanForeignKeys groups (constraint, column, referenced schema, referenced
// table, referenced column) rows, ordered by constraint and key position, into
// foreign keys.
func scanForeignKeys(rows *sql.Rows) ([]entity.ForeignKey, error) {
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
