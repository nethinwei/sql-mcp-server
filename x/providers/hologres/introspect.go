package hologres

import (
	"context"
	"database/sql"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/entity"
)

// hgIntrospector discovers entities from Hologres catalogs. Table listing uses
// pg_catalog directly instead of information_schema: joining the two is
// restricted on older Hologres versions, and only pg_catalog exposes the
// inheritance edge needed to hide LIST-partition child tables (parents appear
// as relkind 'p', or 'r' depending on engine version; children are ordinary
// rows linked through pg_inherits). Column and primary key metadata come from
// information_schema, whose views Hologres fully supports. Internal schemas
// (pg_catalog, information_schema, hologres, hologres_statistic,
// hologres_streaming_mv) never appear because Discover only visits the schemas
// the deployment exposes.
type hgIntrospector struct {
	db *sql.DB
}

// Discover implements introspect.Introspector. It lists base tables in the
// given schemas (defaulting to "public") with their columns and primary keys.
func (i hgIntrospector) Discover(ctx context.Context, sources []string) ([]entity.Entity, error) {
	if len(sources) == 0 {
		sources = []string{"public"}
	}
	trows, err := i.db.QueryContext(ctx,
		`SELECT n.nspname, c.relname
		 FROM pg_catalog.pg_class c
		 JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		 WHERE c.relkind IN ('r', 'p') AND n.nspname = ANY($1)
		   AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_inherits inh WHERE inh.inhrelid = c.oid)
		 ORDER BY n.nspname, c.relname`, sources)
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

func (i hgIntrospector) columns(ctx context.Context, schema, table string) ([]entity.Attribute, []entity.Key, error) {
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

func (i hgIntrospector) primaryKey(ctx context.Context, schema, table string) ([]entity.Key, error) {
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
