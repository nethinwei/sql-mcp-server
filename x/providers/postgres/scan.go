package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/x/providers/sqladapter"
)

// A scan reads each kind of metadata of a set of schemas in one query (see
// sqladapter.Scanned).

type (
	tableKey = sqladapter.TableKey
	scanned  = sqladapter.Scanned
)

// each runs query with names as $1, then args, and calls scan for every row.
func (i pgIntrospector) each(
	ctx context.Context, query string, names []string, scan func(*sql.Rows) error, args ...any,
) error {
	return sqladapter.Each(ctx, i.db, query, append([]any{names}, args...), scan)
}

// Discover implements introspect.Introspector. It lists the relations of the
// given schemas (by default the current one, where unqualified names resolve)
// with their columns, keys, foreign keys and write effects.
func (i pgIntrospector) Discover(ctx context.Context, sources []string) ([]entity.Entity, error) {
	if err := i.gate.check(ctx); err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		current, err := i.currentSchema(ctx)
		if err != nil || current == "" {
			return nil, err
		}
		sources = []string{current}
	}
	tables, err := i.tables(ctx, sources)
	if err != nil || len(tables) == 0 {
		return []entity.Entity{}, err
	}
	s, err := i.scan(ctx, sources)
	if err != nil {
		return nil, err
	}
	entities := make([]entity.Entity, 0, len(tables))
	for _, t := range tables {
		e := entity.Entity{
			Name: t.name, Source: t.name, Schema: t.schema, Description: t.comment, Kind: pgKind(t.kind),
			Derived: t.kind == "v" || t.kind == "m" || t.kind == "f",
		}
		s.Fill(tableKey{Schema: t.schema, Table: t.name}, &e)
		entities = append(entities, e)
	}
	return entities, nil
}

func (i pgIntrospector) scan(ctx context.Context, schemas []string) (*scanned, error) {
	s := sqladapter.NewScanned()
	for _, load := range []func(context.Context, *scanned, []string) error{
		i.columns(columnsSQL), i.columns(matviewColumnsSQL), i.indexes, i.foreignKeys, i.cascades, i.writeEffects,
	} {
		if err := load(ctx, s, schemas); err != nil {
			return nil, err
		}
	}
	return s, nil
}

type pgTable struct{ schema, name, kind, comment string }

// tables lists the tables (plain, partitioned and foreign), views and
// materialized views of schemas that the connection can see, the way
// information_schema.tables does. Partitions are skipped: their data is read
// through the partitioned table, and exposing both would duplicate it. The
// config store's own tables are skipped too.
func (i pgIntrospector) tables(ctx context.Context, schemas []string) ([]pgTable, error) {
	var out []pgTable
	err := i.each(ctx,
		`SELECT n.nspname, c.relname, c.relkind::text, COALESCE(obj_description(c.oid, 'pg_class'), '')
		 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE c.relkind IN ('r', 'p', 'f', 'v', 'm') AND NOT c.relispartition AND n.nspname = ANY($1)
		   AND (pg_has_role(c.relowner, 'USAGE')
		     OR has_table_privilege(c.oid, 'SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER')
		     OR has_any_column_privilege(c.oid, 'SELECT, INSERT, UPDATE, REFERENCES'))`,
		schemas, func(rows *sql.Rows) error {
			var t pgTable
			if err := rows.Scan(&t.schema, &t.name, &t.kind, &t.comment); err != nil {
				return err
			}
			if !config.IsStoreTable(t.name) {
				out = append(out, t)
			}
			return nil
		})
	return out, err
}

// columnsSQL reads columns from information_schema; matviewColumnsSQL reads
// materialized views' columns from pg_attribute, since information_schema
// leaves materialized views out. Both report the same type names, and whether
// the database supplies a value (default), computes it (generated, identity
// ALWAYS) or takes it from a sequence (serial, identity).
const (
	columnsSQL = `SELECT c.table_schema, c.table_name, c.column_name, c.data_type, c.is_nullable,
		        COALESCE(col_description(r.oid, c.ordinal_position::int), ''),
		        c.column_default IS NOT NULL OR c.is_identity = 'YES',
		        c.is_generated = 'ALWAYS' OR COALESCE(c.identity_generation = 'ALWAYS', false),
		        c.is_identity = 'YES' OR COALESCE(c.column_default LIKE 'nextval(%', false)
		 FROM information_schema.columns c
		 JOIN pg_namespace n ON n.nspname = c.table_schema
		 JOIN pg_class r ON r.relnamespace = n.oid AND r.relname = c.table_name
		 WHERE c.table_schema = ANY($1)
		 ORDER BY c.table_schema, c.table_name, c.ordinal_position`
	matviewColumnsSQL = `SELECT n.nspname, c.relname, a.attname, format_type(a.atttypid, NULL),
		        CASE WHEN a.attnotnull THEN 'NO' ELSE 'YES' END, COALESCE(col_description(a.attrelid, a.attnum), ''),
		        false, false, false
		 FROM pg_attribute a
		 JOIN pg_class c ON c.oid = a.attrelid
		 JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE c.relkind = 'm' AND n.nspname = ANY($1) AND a.attnum > 0 AND NOT a.attisdropped
		 ORDER BY n.nspname, c.relname, a.attnum`
)

func (i pgIntrospector) columns(query string) func(context.Context, *scanned, []string) error {
	return func(ctx context.Context, s *scanned, schemas []string) error {
		return i.each(ctx, query, schemas, s.ScanColumn)
	}
}

// indexesSQL lists every valid index with its key parts as the database
// prints them and, for a unique one (a unique constraint is backed by one; an
// index may exist without a constraint), what decides whether it identifies a
// row. INCLUDE columns (past indnkeyatts) are not key parts.
const indexesSQL = `SELECT tn.nspname, t.relname, c.relname, am.amname, i.indisunique, i.indisprimary,
	        NOT i.indimmediate, COALESCE(pg_get_expr(i.indpred, i.indrelid, true), ''),
	        i.indexprs IS NOT NULL OR bool_or(k.attnum = 0),
	        COALESCE((to_jsonb(i) ->> 'indnullsnotdistinct')::boolean, false),
	        bool_and(COALESCE(a.attnotnull, false)),
	        COALESCE(json_agg(a.attname ORDER BY k.ord) FILTER (WHERE a.attname IS NOT NULL), '[]')::text,
	        json_agg(pg_get_indexdef(i.indexrelid, k.ord::int, true) ORDER BY k.ord)::text
	 FROM pg_index i
	 JOIN pg_class c ON c.oid = i.indexrelid
	 JOIN pg_am am ON am.oid = c.relam
	 JOIN pg_class t ON t.oid = i.indrelid
	 JOIN pg_namespace tn ON tn.oid = t.relnamespace
	 CROSS JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord)
	 LEFT JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
	 WHERE tn.nspname = ANY($1) AND i.indisvalid AND k.ord <= i.indnkeyatts
	 GROUP BY i.indexrelid, tn.nspname, t.relname, c.relname, am.amname, i.indisunique, i.indisprimary,
	          i.indimmediate, i.indpred, i.indrelid, i.indexprs IS NOT NULL, to_jsonb(i) ->> 'indnullsnotdistinct'
	 ORDER BY tn.nspname, t.relname, i.indisprimary DESC, c.relname`

// indexes reads every index. The primary key and the unique indexes are also
// the keys, marked when they cannot identify a row (see entity.Key).
func (i pgIntrospector) indexes(ctx context.Context, s *scanned, schemas []string) error {
	return i.each(ctx, indexesSQL, schemas, func(rows *sql.Rows) error {
		var t tableKey
		var ix entity.Index
		var deferrable, expression, nullsNotDistinct, notNull bool
		var columns, parts string
		if err := rows.Scan(&t.Schema, &t.Table, &ix.Name, &ix.Method, &ix.Unique, &ix.Primary, &deferrable,
			&ix.Where, &expression, &nullsNotDistinct, &notNull, &columns, &parts); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(parts), &ix.Parts); err != nil {
			return err
		}
		s.Indexes[t] = append(s.Indexes[t], ix)
		if !ix.Unique {
			return nil
		}
		k := entity.Key{Name: ix.Name, Primary: ix.Primary, Deferrable: deferrable,
			Reason: uniqueKeyReason(ix.Where != "", expression, notNull || nullsNotDistinct)}
		if err := json.Unmarshal([]byte(columns), &k.Columns); err != nil {
			return err
		}
		s.Keys[t] = append(s.Keys[t], k)
		return nil
	})
}

// foreignKeys reads foreign keys with columns in key order.
func (i pgIntrospector) foreignKeys(ctx context.Context, s *scanned, schemas []string) error {
	return i.each(ctx,
		`SELECT n.nspname, cl.relname, con.conname, a.attname, rn.nspname, rc.relname, ra.attname,
		        con.confdeltype::text, con.confupdtype::text
		 FROM pg_constraint con
		 JOIN pg_class cl ON cl.oid = con.conrelid
		 JOIN pg_namespace n ON n.oid = cl.relnamespace
		 CROSS JOIN LATERAL unnest(con.conkey, con.confkey) WITH ORDINALITY AS k(col, refcol, ord)
		 JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.col
		 JOIN pg_class rc ON rc.oid = con.confrelid
		 JOIN pg_namespace rn ON rn.oid = rc.relnamespace
		 JOIN pg_attribute ra ON ra.attrelid = con.confrelid AND ra.attnum = k.refcol
		 WHERE con.contype = 'f' AND n.nspname = ANY($1)
		 ORDER BY n.nspname, cl.relname, con.conname, k.ord`,
		schemas, func(rows *sql.Rows) error { return s.ScanForeignKeyColumn(rows, pgAction) })
}

// cascadesSQL lists, by referenced relation, the foreign keys of other
// relations whose actions write their rows when rows of the referenced one are
// deleted or their key updated. It groups by every column it orders by:
// PostgreSQL before 14 (whose catalogs have no primary keys) and the servers
// of its lineage (Hologres) do not take con.conname as determined by con.oid.
const cascadesSQL = `SELECT pn.nspname, pc.relname, cn.nspname, cc.relname, con.confdeltype::text,
	        con.confupdtype::text, json_agg(pa.attname ORDER BY k.ord)::text, json_agg(fa.attname ORDER BY k.ord)::text
	 FROM pg_constraint con
	 JOIN pg_class cc ON cc.oid = con.conrelid
	 JOIN pg_namespace cn ON cn.oid = cc.relnamespace
	 JOIN pg_class pc ON pc.oid = con.confrelid
	 JOIN pg_namespace pn ON pn.oid = pc.relnamespace
	 CROSS JOIN LATERAL unnest(con.confkey, con.conkey) WITH ORDINALITY AS k(attnum, fattnum, ord)
	 JOIN pg_attribute pa ON pa.attrelid = con.confrelid AND pa.attnum = k.attnum
	 JOIN pg_attribute fa ON fa.attrelid = con.conrelid AND fa.attnum = k.fattnum
	 WHERE con.contype = 'f' AND pn.nspname = ANY($1)
	   AND (con.confdeltype IN ('c', 'n', 'd') OR con.confupdtype IN ('c', 'n', 'd'))
	 GROUP BY con.oid, con.conname, pn.nspname, pc.relname, cn.nspname, cc.relname, con.confdeltype,
	          con.confupdtype
	 ORDER BY pn.nspname, pc.relname, cn.nspname, cc.relname, con.conname`

func (i pgIntrospector) cascades(ctx context.Context, s *scanned, schemas []string) error {
	return i.each(ctx, cascadesSQL, schemas, func(rows *sql.Rows) error {
		var t tableKey
		var c entity.Cascade
		var onDelete, onUpdate, columns, foreign string
		if err := rows.Scan(&t.Schema, &t.Table, &c.Schema, &c.Table, &onDelete, &onUpdate,
			&columns, &foreign); err != nil {
			return err
		}
		if err := errors.Join(json.Unmarshal([]byte(columns), &c.Columns),
			json.Unmarshal([]byte(foreign), &c.ForeignColumns)); err != nil {
			return err
		}
		c.OnDelete, c.OnUpdate = pgAction[onDelete], pgAction[onUpdate]
		s.Cascades[t] = append(s.Cascades[t], c)
		return nil
	})
}

// writeEffects marks the relations with user triggers or rewrite rules,
// whose effects are unknown.
func (i pgIntrospector) writeEffects(ctx context.Context, s *scanned, schemas []string) error {
	return i.each(ctx,
		`SELECT n.nspname, c.relname FROM pg_trigger t
		 JOIN pg_class c ON c.oid = t.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE NOT t.tgisinternal AND n.nspname = ANY($1)
		 UNION SELECT schemaname, tablename FROM pg_rules WHERE schemaname = ANY($1)`,
		schemas, s.ScanSideEffect)
}
