package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
)

// pgIntrospector discovers entities from information_schema.
type pgIntrospector struct {
	db *sql.DB
}

// Schemas implements introspect.SchemaLister: every schema except the system
// ones, and the current schema (the first one of search_path that exists).
func (i pgIntrospector) Schemas(ctx context.Context) ([]string, string, error) {
	rows, err := i.db.QueryContext(ctx,
		`SELECT schema_name FROM information_schema.schemata
		 WHERE schema_name NOT IN ('pg_catalog', 'information_schema')
		   AND schema_name NOT LIKE 'pg\_toast%' AND schema_name NOT LIKE 'pg\_temp\_%'
		 ORDER BY schema_name`)
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
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	current, err := i.currentSchema(ctx)
	return out, current, err
}

func (i pgIntrospector) currentSchema(ctx context.Context) (string, error) {
	var current sql.NullString
	err := i.db.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&current)
	return current.String, err
}

type pgTable struct{ schema, name, kind string }

// tables lists the tables (plain, partitioned and foreign), views and
// materialized views of schemas that the connection can see, the way
// information_schema.tables does. Partitions are skipped: their data is read
// through the partitioned table, and exposing both would duplicate it. The
// config store's own tables are skipped too.
func (i pgIntrospector) tables(ctx context.Context, schemas []string) ([]pgTable, error) {
	rows, err := i.db.QueryContext(ctx,
		`SELECT n.nspname, c.relname, c.relkind::text
		 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE c.relkind IN ('r', 'p', 'f', 'v', 'm') AND NOT c.relispartition AND n.nspname = ANY($1)
		   AND (pg_has_role(c.relowner, 'USAGE')
		     OR has_table_privilege(c.oid, 'SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER')
		     OR has_any_column_privilege(c.oid, 'SELECT, INSERT, UPDATE, REFERENCES'))`,
		schemas)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []pgTable
	for rows.Next() {
		var t pgTable
		if err := rows.Scan(&t.schema, &t.name, &t.kind); err != nil {
			return nil, err
		}
		if !config.IsStoreTable(t.name) {
			out = append(out, t)
		}
	}
	return out, rows.Err()
}

// Discover implements introspect.Introspector. It lists the relations of the
// given schemas (by default the current one, where unqualified names resolve)
// with their columns and primary keys.
func (i pgIntrospector) Discover(ctx context.Context, sources []string) ([]entity.Entity, error) {
	if len(sources) == 0 {
		current, err := i.currentSchema(ctx)
		if err != nil || current == "" {
			return nil, err
		}
		sources = []string{current}
	}
	tables, err := i.tables(ctx, sources)
	if err != nil {
		return nil, err
	}
	entities := make([]entity.Entity, 0, len(tables))
	for _, t := range tables {
		attrs, keys, err := i.columns(ctx, t.schema, t.name, t.kind == "m")
		if err != nil {
			return nil, err
		}
		comment, fks, err := i.tableMeta(ctx, t.schema, t.name)
		if err != nil {
			return nil, err
		}
		cascades, sideEffects, err := i.writeEffects(ctx, t.schema, t.name)
		if err != nil {
			return nil, err
		}
		entities = append(entities, entity.Entity{
			Name:        t.name,
			Source:      t.name,
			Schema:      t.schema,
			Description: comment,
			Kind:        pgKind(t.kind),
			Derived:     t.kind == "v" || t.kind == "m" || t.kind == "f",
			Attributes:  attrs,
			Keys:        keys,
			ForeignKeys: fks,
			Cascades:    cascades,
			SideEffects: sideEffects,
		})
	}
	return entities, nil
}

// pgKind maps a pg_class relkind to the entity kind.
func pgKind(relkind string) entity.Kind {
	if relkind == "v" || relkind == "m" {
		return entity.KindView
	}
	return entity.KindTable
}

// columnsSQL reads columns from information_schema; matviewColumnsSQL reads a
// materialized view's columns from pg_attribute, since information_schema
// leaves materialized views out. Both report the same type names, and whether
// the database supplies a value (default), computes it (generated, identity
// ALWAYS) or takes it from a sequence (serial, identity).
const (
	columnsSQL = `SELECT column_name, data_type, is_nullable,
		        COALESCE(col_description(format('%I.%I', table_schema, table_name)::regclass,
		                                 ordinal_position::int), ''),
		        column_default IS NOT NULL OR is_identity = 'YES',
		        is_generated = 'ALWAYS' OR COALESCE(identity_generation = 'ALWAYS', false),
		        is_identity = 'YES' OR COALESCE(column_default LIKE 'nextval(%', false)
		 FROM information_schema.columns
		 WHERE table_schema=$1 AND table_name=$2 ORDER BY ordinal_position`
	matviewColumnsSQL = `SELECT a.attname, format_type(a.atttypid, NULL),
		        CASE WHEN a.attnotnull THEN 'NO' ELSE 'YES' END, COALESCE(col_description(a.attrelid, a.attnum), ''),
		        false, false, false
		 FROM pg_attribute a
		 WHERE a.attrelid = format('%I.%I', $1::text, $2::text)::regclass AND a.attnum > 0 AND NOT a.attisdropped
		 ORDER BY a.attnum`
	// keysSQL lists the primary key and every valid unique index (a unique
	// constraint is backed by one; an index may exist without a constraint),
	// with what decides whether it identifies a row. INCLUDE columns (past
	// indnkeyatts) are not part of the key.
	keysSQL = `SELECT c.relname, i.indisprimary, NOT i.indimmediate, i.indpred IS NOT NULL,
		        i.indexprs IS NOT NULL OR bool_or(k.attnum = 0),
		        COALESCE((to_jsonb(i) ->> 'indnullsnotdistinct')::boolean, false),
		        bool_and(COALESCE(a.attnotnull, false)),
		        COALESCE(json_agg(a.attname ORDER BY k.ord) FILTER (WHERE a.attname IS NOT NULL), '[]')::text
		 FROM pg_index i
		 JOIN pg_class c ON c.oid = i.indexrelid
		 CROSS JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord)
		 LEFT JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
		 WHERE i.indrelid = format('%I.%I', $1::text, $2::text)::regclass AND i.indisunique AND i.indisvalid
		   AND k.ord <= i.indnkeyatts
		 GROUP BY c.relname, i.indisprimary, i.indimmediate, i.indpred IS NOT NULL, i.indexprs IS NOT NULL,
		          to_jsonb(i) ->> 'indnullsnotdistinct'
		 ORDER BY i.indisprimary DESC, c.relname`
)

func (i pgIntrospector) columns(
	ctx context.Context,
	schema, table string,
	matview bool,
) ([]entity.Attribute, []entity.Key, error) {
	query := columnsSQL
	if matview {
		query = matviewColumnsSQL
	}
	crows, err := i.db.QueryContext(ctx, query, schema, table)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = crows.Close() }()
	var attrs []entity.Attribute
	for crows.Next() {
		var name, dataType, nullable, comment string
		var domain entity.Domain
		if err := crows.Scan(&name, &dataType, &nullable, &comment,
			&domain.Default, &domain.Generated, &domain.AutoIncrement); err != nil {
			return nil, nil, err
		}
		domain.Type, domain.Nullable = dataType, strings.EqualFold(nullable, "YES")
		attrs = append(attrs, entity.Attribute{Name: name, Description: comment, Domain: domain})
	}
	if err := crows.Err(); err != nil {
		return nil, nil, err
	}
	keys, err := i.keys(ctx, schema, table)
	if err != nil {
		return nil, nil, err
	}
	return attrs, keys, nil
}

// keys reads the primary key and unique keys, marking the unique keys that
// cannot identify a row (see entity.Key).
func (i pgIntrospector) keys(ctx context.Context, schema, table string) ([]entity.Key, error) {
	rows, err := i.db.QueryContext(ctx, keysSQL, schema, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var keys []entity.Key
	for rows.Next() {
		var k entity.Key
		var partial, expression, nullsNotDistinct, notNull bool
		var columns string
		if err := rows.Scan(&k.Name, &k.Primary, &k.Deferrable, &partial, &expression,
			&nullsNotDistinct, &notNull, &columns); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(columns), &k.Columns); err != nil {
			return nil, err
		}
		k.Reason = uniqueKeyReason(partial, expression, notNull || nullsNotDistinct)
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// uniqueKeyReason says why a unique index cannot identify a row, or "".
func uniqueKeyReason(partial, expression, nullsDistinctFree bool) string {
	switch {
	case partial:
		return entity.KeyPartial
	case expression:
		return entity.KeyExpression
	case !nullsDistinctFree:
		return entity.KeyNullable
	}
	return ""
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
		`SELECT con.conname, a.attname, rn.nspname, rc.relname, ra.attname,
		        con.confdeltype::text, con.confupdtype::text
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

// pgAction maps pg_constraint confdeltype/confupdtype codes; a (no action)
// and r (restrict) only check and map to "".
var pgAction = map[string]string{"c": entity.FKCascade, "n": entity.FKSetNull, "d": entity.FKSetDefault}

// scanForeignKeys groups (constraint, column, referenced schema, referenced
// table, referenced column) rows, ordered by constraint and key position, into
// foreign keys.
func scanForeignKeys(rows *sql.Rows) ([]entity.ForeignKey, error) {
	var fks []entity.ForeignKey
	for rows.Next() {
		var name, col, refSchema, refTable, refCol, onDelete, onUpdate string
		if err := rows.Scan(&name, &col, &refSchema, &refTable, &refCol, &onDelete, &onUpdate); err != nil {
			return nil, err
		}
		if n := len(fks); n == 0 || fks[n-1].Name != name {
			fks = append(fks, entity.ForeignKey{
				Name: name, RefSchema: refSchema, RefRelation: refTable,
				OnDelete: pgAction[onDelete], OnUpdate: pgAction[onUpdate],
			})
		}
		last := &fks[len(fks)-1]
		last.Columns = append(last.Columns, col)
		last.RefColumns = append(last.RefColumns, refCol)
	}
	return fks, rows.Err()
}

// Physical implements introspect.PhysicalNamer. The cluster's system
// identifier names the server; a role that cannot read it falls back to the
// server address, and to "" (unknown) over a Unix socket.
func (i pgIntrospector) Physical(ctx context.Context) (introspect.Physical, error) {
	var p introspect.Physical
	if err := i.db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&p.Catalog); err != nil {
		return p, err
	}
	if err := i.db.QueryRowContext(ctx,
		`SELECT system_identifier::text FROM pg_control_system()`).Scan(&p.Server); err == nil {
		return p, nil
	}
	var addr sql.NullString
	err := i.db.QueryRowContext(ctx,
		`SELECT host(inet_server_addr()) || ':' || inet_server_port()`).Scan(&addr)
	p.Server = addr.String
	return p, err
}

// cascadesSQL lists the foreign keys of other relations whose actions write
// their rows when rows of this one are deleted or their key updated.
const cascadesSQL = `SELECT cn.nspname, cc.relname, con.confdeltype::text, con.confupdtype::text,
	        json_agg(pa.attname ORDER BY k.ord)::text, json_agg(fa.attname ORDER BY k.ord)::text
	 FROM pg_constraint con
	 JOIN pg_class cc ON cc.oid = con.conrelid
	 JOIN pg_namespace cn ON cn.oid = cc.relnamespace
	 CROSS JOIN LATERAL unnest(con.confkey, con.conkey) WITH ORDINALITY AS k(attnum, fattnum, ord)
	 JOIN pg_attribute pa ON pa.attrelid = con.confrelid AND pa.attnum = k.attnum
	 JOIN pg_attribute fa ON fa.attrelid = con.conrelid AND fa.attnum = k.fattnum
	 WHERE con.contype = 'f' AND con.confrelid = format('%I.%I', $1::text, $2::text)::regclass
	   AND (con.confdeltype IN ('c', 'n', 'd') OR con.confupdtype IN ('c', 'n', 'd'))
	 GROUP BY con.oid, cn.nspname, cc.relname, con.confdeltype, con.confupdtype
	 ORDER BY cn.nspname, cc.relname, con.conname`

// writeEffects reads what a write to the relation does beyond it: cascading
// foreign keys of other relations, and user triggers or rewrite rules.
func (i pgIntrospector) writeEffects(ctx context.Context, schema, table string) ([]entity.Cascade, bool, error) {
	rows, err := i.db.QueryContext(ctx, cascadesSQL, schema, table)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	var cascades []entity.Cascade
	for rows.Next() {
		var c entity.Cascade
		var onDelete, onUpdate, columns, foreign string
		if err := rows.Scan(&c.Schema, &c.Table, &onDelete, &onUpdate, &columns, &foreign); err != nil {
			return nil, false, err
		}
		if err := errors.Join(json.Unmarshal([]byte(columns), &c.Columns),
			json.Unmarshal([]byte(foreign), &c.ForeignColumns)); err != nil {
			return nil, false, err
		}
		c.OnDelete, c.OnUpdate = pgAction[onDelete], pgAction[onUpdate]
		cascades = append(cascades, c)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	var sideEffects bool
	err = i.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_trigger
		                WHERE tgrelid = format('%I.%I', $1::text, $2::text)::regclass AND NOT tgisinternal)
		     OR EXISTS (SELECT 1 FROM pg_rules WHERE schemaname = $1 AND tablename = $2)`,
		schema, table).Scan(&sideEffects)
	return cascades, sideEffects, err
}
