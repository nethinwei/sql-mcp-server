package postgres

import (
	"context"
	"database/sql"
	"strings"

	"github.com/nethinwei/sql-mcp-server/entity"
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
		entities = append(entities, entity.Entity{
			Name:       t.name,
			Source:     t.name,
			Schema:     t.schema,
			Kind:       entity.KindTable,
			Attributes: attrs,
			Keys:       keys,
		})
	}
	return entities, nil
}

func (i pgIntrospector) columns(ctx context.Context, schema, table string) ([]entity.Attribute, []entity.Key, error) {
	crows, err := i.db.QueryContext(ctx,
		`SELECT column_name, data_type, is_nullable
		 FROM information_schema.columns
		 WHERE table_schema=$1 AND table_name=$2 ORDER BY ordinal_position`, schema, table)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = crows.Close() }()
	var attrs []entity.Attribute
	for crows.Next() {
		var name, dataType, nullable string
		if err := crows.Scan(&name, &dataType, &nullable); err != nil {
			return nil, nil, err
		}
		attrs = append(attrs, entity.Attribute{
			Name: name,
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
