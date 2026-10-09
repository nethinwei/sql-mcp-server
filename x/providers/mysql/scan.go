package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/x/providers/sqladapter"
)

// A scan reads each kind of metadata of a set of schemas in one query, the
// way mysqldump reads a database: the number of round trips does not grow with
// the number of tables, and information_schema is only read for the scanned
// schemas.

// tableKey names a table across the schemas of one scan.
type tableKey struct{ schema, table string }

// scanned is the metadata of the scanned schemas, by table.
type scanned struct {
	attrs    map[tableKey][]entity.Attribute
	keys     map[tableKey][]entity.Key
	indexes  map[tableKey][]entity.Index
	fks      map[tableKey][]entity.ForeignKey
	cascades map[tableKey][]entity.Cascade
	triggers map[tableKey]bool
}

// forSchemas fills the {schemas} placeholder of query with one parameter per
// schema.
func forSchemas(query string, schemas []string) (string, []any) {
	args := make([]any, len(schemas))
	for n, s := range schemas {
		args[n] = s
	}
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(schemas)), ", ")
	return strings.ReplaceAll(query, "{schemas}", marks), args
}

// each runs query over schemas and calls scan for every row.
func (i Introspector) each(ctx context.Context, query string, schemas []string, scan func(*sql.Rows) error) error {
	q, args := forSchemas(query, schemas)
	return sqladapter.Each(ctx, i.db, q, args, scan)
}

// Discover implements introspect.Introspector. It lists base tables and views
// of the requested schemas (by default the current database, where
// unqualified names resolve) with their columns, keys, foreign keys and write
// effects.
func (i Introspector) Discover(ctx context.Context, sources []string) ([]entity.Entity, error) {
	if len(sources) == 0 {
		current, err := i.currentDatabase(ctx)
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
		k := tableKey{t.schema, t.name}
		kind := entity.KindTable
		if t.view {
			kind = entity.KindView
		}
		entities = append(entities, entity.Entity{
			Name: t.name, Source: t.name, Schema: t.schema, Description: t.comment,
			Kind: kind, Derived: t.view, Attributes: s.attrs[k], Keys: s.keys[k], ForeignKeys: s.fks[k],
			Indexes: s.indexes[k], Cascades: s.cascades[k], SideEffects: s.triggers[k],
		})
	}
	return entities, nil
}

func (i Introspector) scan(ctx context.Context, schemas []string) (scanned, error) {
	s := scanned{
		attrs: map[tableKey][]entity.Attribute{}, keys: map[tableKey][]entity.Key{},
		indexes: map[tableKey][]entity.Index{}, fks: map[tableKey][]entity.ForeignKey{},
		cascades: map[tableKey][]entity.Cascade{}, triggers: map[tableKey]bool{},
	}
	for _, load := range []func(context.Context, *scanned, []string) error{
		i.columns, i.indexes, i.foreignKeys, i.writeEffects, i.triggers,
	} {
		if err := load(ctx, &s, schemas); err != nil {
			return s, err
		}
	}
	return s, nil
}

type mysqlTable struct {
	schema, name, comment string
	view                  bool
}

// tables lists the base tables and views of schemas, skipping the config
// store's own.
func (i Introspector) tables(ctx context.Context, schemas []string) ([]mysqlTable, error) {
	var tables []mysqlTable
	err := i.each(ctx,
		`SELECT table_schema, table_name, COALESCE(table_comment, ''), table_type = 'VIEW'
		 FROM information_schema.tables
		 WHERE table_schema IN ({schemas}) AND table_type IN ('BASE TABLE', 'VIEW')`,
		schemas, func(rows *sql.Rows) error {
			var t mysqlTable
			if err := rows.Scan(&t.schema, &t.name, &t.comment, &t.view); err != nil {
				return err
			}
			if !contains(schemas, t.schema) || config.IsStoreTable(t.name) {
				return nil
			}
			if t.view && t.comment == "VIEW" { // MySQL reports a view's comment as "VIEW"
				t.comment = ""
			}
			tables = append(tables, t)
			return nil
		})
	return tables, err
}

// columnsSQL reads columns with whether the database supplies a value
// (default, auto_increment), computes it (generated column) or takes it from a
// sequence (auto_increment). DEFAULT_GENERATED marks a default expression.
const columnsSQL = `SELECT table_schema, table_name, column_name, data_type, is_nullable,
	        COALESCE(column_comment, ''),
	        column_default IS NOT NULL OR extra LIKE '%auto_increment%',
	        extra LIKE '%VIRTUAL GENERATED%' OR extra LIKE '%STORED GENERATED%',
	        extra LIKE '%auto_increment%'
	 FROM information_schema.columns
	 WHERE table_schema IN ({schemas}) ORDER BY table_schema, table_name, ordinal_position`

func (i Introspector) columns(ctx context.Context, s *scanned, schemas []string) error {
	return i.each(ctx, columnsSQL, schemas, func(rows *sql.Rows) error {
		var k tableKey
		var name, dataType, nullable, comment string
		var domain entity.Domain
		if err := rows.Scan(&k.schema, &k.table, &name, &dataType, &nullable, &comment,
			&domain.Default, &domain.Generated, &domain.AutoIncrement); err != nil {
			return err
		}
		domain.Type, domain.Nullable = dataType, strings.EqualFold(nullable, "YES")
		s.attrs[k] = append(s.attrs[k], entity.Attribute{Name: name, Description: comment, Domain: domain})
		return nil
	})
}

// indexes reads every index. The primary key and the unique indexes (MySQL
// enforces uniqueness through indexes) are also the keys, marked when they
// cannot identify a row: an index on an expression (no column name), a
// prefix of a column, or a nullable column.
func (i Introspector) indexes(ctx context.Context, s *scanned, schemas []string) error {
	return i.each(ctx,
		`SELECT table_schema, table_name, index_name, LOWER(index_type), non_unique = 0,
		        COALESCE(column_name, ''), sub_part, nullable = 'YES'
		 FROM information_schema.statistics
		 WHERE table_schema IN ({schemas})
		 ORDER BY table_schema, table_name, index_name = 'PRIMARY' DESC, index_name, seq_in_index`,
		schemas, func(rows *sql.Rows) error {
			var k tableKey
			var ix entity.Index
			var column string
			var prefix sql.NullInt64
			var nullable bool
			if err := rows.Scan(&k.schema, &k.table, &ix.Name, &ix.Method, &ix.Unique, &column, &prefix,
				&nullable); err != nil {
				return err
			}
			indexes := s.indexes[k]
			if n := len(indexes); n == 0 || indexes[n-1].Name != ix.Name {
				ix.Primary = ix.Name == "PRIMARY"
				indexes = append(indexes, ix)
				if ix.Unique {
					s.keys[k] = append(s.keys[k], entity.Key{Name: ix.Name, Primary: ix.Primary})
				}
			}
			part := column
			// A spatial index reports the whole geometry's length as a prefix.
			if prefix.Valid && ix.Method != "spatial" {
				part = fmt.Sprintf("%s(%d)", column, prefix.Int64)
			}
			last := &indexes[len(indexes)-1]
			last.Parts = append(last.Parts, part)
			s.indexes[k] = indexes
			if last.Unique {
				addKeyPart(&s.keys[k][len(s.keys[k])-1], column, prefix.Valid, nullable)
			}
			return nil
		})
}

func addKeyPart(key *entity.Key, column string, prefix, nullable bool) {
	switch {
	case column == "":
		key.Reason = entity.KeyExpression
	case prefix && key.Reason == "":
		key.Reason = entity.KeyPrefix
	case nullable && key.Reason == "":
		key.Reason = entity.KeyNullable
	}
	if column != "" {
		key.Columns = append(key.Columns, column)
	}
}

// mysqlAction maps referential rules; RESTRICT and NO ACTION only check.
var mysqlAction = map[string]string{
	"CASCADE": entity.FKCascade, "SET NULL": entity.FKSetNull, "SET DEFAULT": entity.FKSetDefault,
}

// foreignKeys lists foreign keys with columns in key order.
func (i Introspector) foreignKeys(ctx context.Context, s *scanned, schemas []string) error {
	return i.each(ctx,
		`SELECT k.table_schema, k.table_name, k.constraint_name, k.column_name, k.referenced_table_schema,
		        k.referenced_table_name, k.referenced_column_name, r.delete_rule, r.update_rule
		 FROM information_schema.key_column_usage k
		 JOIN information_schema.referential_constraints r
		   ON r.constraint_schema = k.constraint_schema AND r.constraint_name = k.constraint_name
		  AND r.table_name = k.table_name
		 WHERE k.table_schema IN ({schemas}) AND k.referenced_table_name IS NOT NULL
		 ORDER BY k.table_schema, k.table_name, k.constraint_name, k.ordinal_position`,
		schemas, func(rows *sql.Rows) error {
			var k tableKey
			var name, col, refSchema, refTable, refCol, onDelete, onUpdate string
			if err := rows.Scan(&k.schema, &k.table, &name, &col, &refSchema, &refTable, &refCol,
				&onDelete, &onUpdate); err != nil {
				return err
			}
			fks := s.fks[k]
			if n := len(fks); n == 0 || fks[n-1].Name != name {
				fks = append(fks, entity.ForeignKey{
					Name: name, RefSchema: refSchema, RefRelation: refTable,
					OnDelete: mysqlAction[onDelete], OnUpdate: mysqlAction[onUpdate],
				})
			}
			last := &fks[len(fks)-1]
			last.Columns = append(last.Columns, col)
			last.RefColumns = append(last.RefColumns, refCol)
			s.fks[k] = fks
			return nil
		})
}

// writeEffects reads, for each referenced table, the cascading foreign keys of
// other tables: what a write to it does beyond it.
func (i Introspector) writeEffects(ctx context.Context, s *scanned, schemas []string) error {
	last := map[tableKey]string{}
	return i.each(ctx,
		`SELECT r.unique_constraint_schema, r.referenced_table_name, r.constraint_name, r.constraint_schema,
		        r.table_name, r.delete_rule, r.update_rule, k.referenced_column_name, k.column_name
		 FROM information_schema.referential_constraints r
		 JOIN information_schema.key_column_usage k
		   ON k.constraint_schema = r.constraint_schema AND k.constraint_name = r.constraint_name
		  AND k.table_name = r.table_name
		 WHERE r.unique_constraint_schema IN ({schemas})
		   AND (r.delete_rule IN ('CASCADE', 'SET NULL', 'SET DEFAULT')
		     OR r.update_rule IN ('CASCADE', 'SET NULL', 'SET DEFAULT'))
		 ORDER BY r.unique_constraint_schema, r.referenced_table_name, r.constraint_schema, r.table_name,
		          r.constraint_name, k.ordinal_position`,
		schemas, func(rows *sql.Rows) error {
			var k tableKey
			var name, onDelete, onUpdate, column, foreign string
			var c entity.Cascade
			if err := rows.Scan(&k.schema, &k.table, &name, &c.Schema, &c.Table, &onDelete, &onUpdate,
				&column, &foreign); err != nil {
				return err
			}
			if id := c.Schema + "." + c.Table + "." + name; id != last[k] {
				last[k] = id
				c.OnDelete, c.OnUpdate = mysqlAction[onDelete], mysqlAction[onUpdate]
				s.cascades[k] = append(s.cascades[k], c)
			}
			cur := &s.cascades[k][len(s.cascades[k])-1]
			cur.Columns, cur.ForeignColumns = append(cur.Columns, column), append(cur.ForeignColumns, foreign)
			return nil
		})
}

// triggers marks the tables with triggers, whose effects are unknown.
func (i Introspector) triggers(ctx context.Context, s *scanned, schemas []string) error {
	return i.each(ctx,
		`SELECT DISTINCT event_object_schema, event_object_table FROM information_schema.triggers
		 WHERE event_object_schema IN ({schemas})`,
		schemas, func(rows *sql.Rows) error {
			var k tableKey
			if err := rows.Scan(&k.schema, &k.table); err != nil {
				return err
			}
			s.triggers[k] = true
			return nil
		})
}
