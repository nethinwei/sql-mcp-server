package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

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
	for _, load := range []struct {
		name string
		read func(context.Context, *scanned, []string) error
	}{
		{"columns", i.columns(columnsSQL)}, {"materialized view columns", i.columns(matviewColumnsSQL)},
		{"indexes", i.indexes}, {"foreign keys", i.foreignKeys}, {"cascades", i.cascades},
		{"triggers and rules", i.writeEffects},
	} {
		if err := load.read(ctx, s, schemas); err != nil {
			return nil, fmt.Errorf("postgres: scan %s: %w", load.name, err)
		}
	}
	return s, nil
}

type pgTable struct{ schema, name, kind, comment string }

// tables lists the tables (plain, partitioned and foreign), views and
// materialized views of schemas that the connection can see, the way
// information_schema.tables does, in the schemas it may use (a table's own
// grants reach nothing in a schema without USAGE). Partitions are skipped: their data is read
// through the partitioned table, and exposing both would duplicate it. The
// config store's own tables are skipped too.
func (i pgIntrospector) tables(ctx context.Context, schemas []string) ([]pgTable, error) {
	var out []pgTable
	err := i.each(ctx,
		`SELECT n.nspname, c.relname, c.relkind::text, COALESCE(obj_description(c.oid, 'pg_class'), '')
		 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE c.relkind IN ('r', 'p', 'f', 'v', 'm') AND NOT c.relispartition AND n.nspname = ANY($1)
		   AND has_schema_privilege(n.oid, 'USAGE')
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
// the database supplies a value (default: the column's or, for a column of a
// domain, the domain's), computes it (generated, identity ALWAYS) or takes it
// from a sequence (serial, identity). information_schema counts a domain's
// NOT NULL in is_nullable, but not its default in column_default.
const (
	columnsSQL = `SELECT c.table_schema, c.table_name, c.column_name, c.data_type, c.is_nullable,
		        COALESCE(col_description(r.oid, c.ordinal_position::int), ''),
		        c.column_default IS NOT NULL OR c.is_identity = 'YES' OR d.typdefault IS NOT NULL,
		        c.is_generated = 'ALWAYS' OR COALESCE(c.identity_generation = 'ALWAYS', false),
		        c.is_identity = 'YES' OR COALESCE(c.column_default LIKE 'nextval(%', false)
		 FROM information_schema.columns c
		 JOIN pg_namespace n ON n.nspname = c.table_schema
		 JOIN pg_class r ON r.relnamespace = n.oid AND r.relname = c.table_name
		 JOIN pg_attribute a ON a.attrelid = r.oid AND a.attname = c.column_name
		 LEFT JOIN pg_type d ON d.oid = a.atttypid AND d.typtype = 'd'
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
	        bool_and(COALESCE(a.attnotnull OR dt.typnotnull, false)),
	        COALESCE(json_agg(a.attname ORDER BY k.ord) FILTER (WHERE a.attname IS NOT NULL), '[]')::text,
	        json_agg(pg_get_indexdef(i.indexrelid, k.ord::int, true) ORDER BY k.ord)::text
	 FROM pg_index i
	 JOIN pg_class c ON c.oid = i.indexrelid
	 JOIN pg_am am ON am.oid = c.relam
	 JOIN pg_class t ON t.oid = i.indrelid
	 JOIN pg_namespace tn ON tn.oid = t.relnamespace
	 CROSS JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord)
	 LEFT JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
	 LEFT JOIN pg_type dt ON dt.oid = a.atttypid -- a domain's NOT NULL (typnotnull) holds for its columns
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

// partitionRootsCTE names, as partition_roots(rel, root), the partitioned
// table each partition belongs to at the top. A partition's constraints and
// triggers act on the writes to that table, the entity (partitions are not
// listed, see tables). Only partitions are followed: a table inheriting
// another the old way is a table of its own. PostgreSQL 11 has no
// pg_partition_root.
const partitionRootsCTE = `WITH RECURSIVE partition_up (rel, parent) AS (
	   SELECT i.inhrelid, i.inhparent FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
	   WHERE c.relispartition
	   UNION ALL
	   SELECT u.rel, i.inhparent FROM partition_up u JOIN pg_class p ON p.oid = u.parent AND p.relispartition
	   JOIN pg_inherits i ON i.inhrelid = u.parent),
	 partition_roots (rel, root) AS (
	   SELECT u.rel, u.parent FROM partition_up u JOIN pg_class p ON p.oid = u.parent AND NOT p.relispartition)
	 `

// foreignKeys reads foreign keys with columns in key order. A key referencing
// a partition references its partitioned table; the copies of a partitioned
// table's key on its partitions (conparentid) are left out.
func (i pgIntrospector) foreignKeys(ctx context.Context, s *scanned, schemas []string) error {
	return i.each(ctx,
		partitionRootsCTE+`SELECT n.nspname, cl.relname, con.conname, a.attname, rn.nspname, rc.relname, ra.attname,
		        con.confdeltype::text, con.confupdtype::text
		 FROM pg_constraint con
		 JOIN pg_class cl ON cl.oid = con.conrelid
		 JOIN pg_namespace n ON n.oid = cl.relnamespace
		 CROSS JOIN LATERAL unnest(con.conkey, con.confkey) WITH ORDINALITY AS k(col, refcol, ord)
		 JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.col
		 LEFT JOIN partition_roots pr ON pr.rel = con.confrelid
		 JOIN pg_class rc ON rc.oid = COALESCE(pr.root, con.confrelid)
		 JOIN pg_namespace rn ON rn.oid = rc.relnamespace
		 JOIN pg_attribute ra ON ra.attrelid = con.confrelid AND ra.attnum = k.refcol
		 WHERE con.contype = 'f' AND con.conparentid = 0 AND n.nspname = ANY($1)
		 ORDER BY n.nspname, cl.relname, con.conname, k.ord`,
		schemas, func(rows *sql.Rows) error { return s.ScanForeignKeyColumn(rows, pgAction) })
}

// cascadesSQL lists, by referenced relation, the foreign keys of other
// relations whose actions write their rows when rows of the referenced one are
// deleted or their key updated. Both relations are taken at their partition
// root: a key referencing a partition writes when rows of its partitioned
// table are deleted, which the gate must then authorize (I-6); the copies of
// a key on partitions (conparentid) are left out, or the writes would seem
// to reach partitions no entity exposes. It groups by every column it orders
// by: PostgreSQL before 14 (whose catalogs have no primary keys) does not
// take con.conname as determined by con.oid.
const cascadesSQL = partitionRootsCTE + `SELECT pn.nspname, pc.relname, cn.nspname, cc.relname, con.confdeltype::text,
	        con.confupdtype::text, json_agg(pa.attname ORDER BY k.ord)::text, json_agg(fa.attname ORDER BY k.ord)::text
	 FROM pg_constraint con
	 LEFT JOIN partition_roots cr ON cr.rel = con.conrelid
	 JOIN pg_class cc ON cc.oid = COALESCE(cr.root, con.conrelid)
	 JOIN pg_namespace cn ON cn.oid = cc.relnamespace
	 LEFT JOIN partition_roots pr ON pr.rel = con.confrelid
	 JOIN pg_class pc ON pc.oid = COALESCE(pr.root, con.confrelid)
	 JOIN pg_namespace pn ON pn.oid = pc.relnamespace
	 CROSS JOIN LATERAL unnest(con.confkey, con.conkey) WITH ORDINALITY AS k(attnum, fattnum, ord)
	 JOIN pg_attribute pa ON pa.attrelid = con.confrelid AND pa.attnum = k.attnum
	 JOIN pg_attribute fa ON fa.attrelid = con.conrelid AND fa.attnum = k.fattnum
	 WHERE con.contype = 'f' AND con.conparentid = 0 AND pn.nspname = ANY($1)
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
// whose effects are unknown; one on a partition marks its partitioned table,
// whose writes run it. Rules are pg_rules' (pg_rewrite but the views'
// _RETURN rules).
func (i pgIntrospector) writeEffects(ctx context.Context, s *scanned, schemas []string) error {
	return i.each(ctx,
		partitionRootsCTE+`SELECT n.nspname, c.relname
		 FROM (SELECT tgrelid AS rel FROM pg_trigger WHERE NOT tgisinternal
		       UNION SELECT ev_class FROM pg_rewrite WHERE rulename <> '_RETURN') w
		 LEFT JOIN partition_roots r ON r.rel = w.rel
		 JOIN pg_class c ON c.oid = COALESCE(r.root, w.rel) JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = ANY($1)
		 GROUP BY n.nspname, c.relname`,
		schemas, s.ScanSideEffect)
}
