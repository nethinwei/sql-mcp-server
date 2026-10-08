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

// Discover implements introspect.Introspector. It lists base tables and views
// (filtered to the requested schemas in memory, by default the current
// database, where unqualified names resolve) with their columns and primary
// keys.
func (i Introspector) Discover(ctx context.Context, sources []string) ([]entity.Entity, error) {
	if len(sources) == 0 {
		current, err := i.currentDatabase(ctx)
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
		attrs, keys, err := i.columns(ctx, t.schema, t.name)
		if err != nil {
			return nil, err
		}
		fks, err := i.foreignKeys(ctx, t.schema, t.name)
		if err != nil {
			return nil, err
		}
		cascades, sideEffects, err := i.writeEffects(ctx, t.schema, t.name)
		if err != nil {
			return nil, err
		}
		kind := entity.KindTable
		if t.view {
			kind = entity.KindView
		}
		entities = append(entities, entity.Entity{
			Name: t.name, Source: t.name, Schema: t.schema, Description: t.comment,
			Kind: kind, Derived: t.view, Attributes: attrs, Keys: keys, ForeignKeys: fks,
			Cascades: cascades, SideEffects: sideEffects,
		})
	}
	return entities, nil
}

// columnsSQL reads columns with whether the database supplies a value
// (default, auto_increment), computes it (generated column) or takes it from a
// sequence (auto_increment). DEFAULT_GENERATED marks a default expression.
const columnsSQL = `SELECT column_name, data_type, is_nullable, COALESCE(column_comment, ''),
	        column_default IS NOT NULL OR extra LIKE '%auto_increment%',
	        extra LIKE '%VIRTUAL GENERATED%' OR extra LIKE '%STORED GENERATED%',
	        extra LIKE '%auto_increment%'
	 FROM information_schema.columns
	 WHERE table_schema=? AND table_name=? ORDER BY ordinal_position`

func (i Introspector) columns(ctx context.Context, schema, table string) ([]entity.Attribute, []entity.Key, error) {
	crows, err := i.db.QueryContext(ctx, columnsSQL, schema, table)
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

// keys reads the primary key and unique indexes (MySQL enforces uniqueness
// through indexes), marking those that cannot identify a row: an index on an
// expression (no column name), a prefix of a column, or a nullable column.
func (i Introspector) keys(ctx context.Context, schema, table string) ([]entity.Key, error) {
	rows, err := i.db.QueryContext(ctx,
		`SELECT index_name, COALESCE(column_name, ''), sub_part IS NOT NULL, nullable = 'YES'
		 FROM information_schema.statistics
		 WHERE table_schema=? AND table_name=? AND non_unique=0
		 ORDER BY index_name = 'PRIMARY' DESC, index_name, seq_in_index`, schema, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var keys []entity.Key
	for rows.Next() {
		var name, column string
		var prefix, nullable bool
		if err := rows.Scan(&name, &column, &prefix, &nullable); err != nil {
			return nil, err
		}
		if n := len(keys); n == 0 || keys[n-1].Name != name {
			keys = append(keys, entity.Key{Name: name, Primary: name == "PRIMARY"})
		}
		k := &keys[len(keys)-1]
		switch {
		case column == "":
			k.Reason = entity.KeyExpression
		case prefix && k.Reason == "":
			k.Reason = entity.KeyPrefix
		case nullable && k.Reason == "":
			k.Reason = entity.KeyNullable
		}
		if column != "" {
			k.Columns = append(k.Columns, column)
		}
	}
	return keys, rows.Err()
}

// mysqlAction maps referential rules; RESTRICT and NO ACTION only check.
var mysqlAction = map[string]string{
	"CASCADE": entity.FKCascade, "SET NULL": entity.FKSetNull, "SET DEFAULT": entity.FKSetDefault,
}

// foreignKeys lists foreign keys with columns in key order.
func (i Introspector) foreignKeys(ctx context.Context, schema, table string) ([]entity.ForeignKey, error) {
	rows, err := i.db.QueryContext(ctx,
		`SELECT k.constraint_name, k.column_name, k.referenced_table_schema, k.referenced_table_name,
		        k.referenced_column_name, r.delete_rule, r.update_rule
		 FROM information_schema.key_column_usage k
		 JOIN information_schema.referential_constraints r
		   ON r.constraint_schema = k.constraint_schema AND r.constraint_name = k.constraint_name
		  AND r.table_name = k.table_name
		 WHERE k.table_schema=? AND k.table_name=? AND k.referenced_table_name IS NOT NULL
		 ORDER BY k.constraint_name, k.ordinal_position`, schema, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var fks []entity.ForeignKey
	for rows.Next() {
		var name, col, refSchema, refTable, refCol, onDelete, onUpdate string
		if err := rows.Scan(&name, &col, &refSchema, &refTable, &refCol, &onDelete, &onUpdate); err != nil {
			return nil, err
		}
		if n := len(fks); n == 0 || fks[n-1].Name != name {
			fks = append(fks, entity.ForeignKey{
				Name: name, RefSchema: refSchema, RefRelation: refTable,
				OnDelete: mysqlAction[onDelete], OnUpdate: mysqlAction[onUpdate],
			})
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

// Physical implements introspect.PhysicalNamer: the server UUID (unknown when
// the server does not report one, as OceanBase may not) and whether table
// names compare case-insensitively.
func (i Introspector) Physical(ctx context.Context) (introspect.Physical, error) {
	var p introspect.Physical
	var lowerCase int
	if err := i.db.QueryRowContext(ctx, `SELECT @@lower_case_table_names`).Scan(&lowerCase); err != nil {
		return p, err
	}
	p.FoldCase = lowerCase != 0
	var uuid sql.NullString
	if err := i.db.QueryRowContext(ctx, `SELECT @@server_uuid`).Scan(&uuid); err == nil {
		p.Server = uuid.String
	}
	return p, nil
}

type mysqlTable struct {
	schema, name, comment string
	view                  bool
}

// tables lists the base tables and views of schemas, skipping the config
// store's own.
func (i Introspector) tables(ctx context.Context, schemas []string) ([]mysqlTable, error) {
	trows, err := i.db.QueryContext(ctx,
		`SELECT table_schema, table_name, COALESCE(table_comment, ''), table_type = 'VIEW'
		 FROM information_schema.tables WHERE table_type IN ('BASE TABLE', 'VIEW')`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = trows.Close() }()
	var tables []mysqlTable
	for trows.Next() {
		var t mysqlTable
		if err := trows.Scan(&t.schema, &t.name, &t.comment, &t.view); err != nil {
			return nil, err
		}
		if !contains(schemas, t.schema) || config.IsStoreTable(t.name) {
			continue
		}
		if t.view && t.comment == "VIEW" { // MySQL reports a view's comment as "VIEW"
			t.comment = ""
		}
		tables = append(tables, t)
	}
	if err := trows.Err(); err != nil {
		return nil, err
	}
	return tables, nil
}

// writeEffects reads what a write to the relation does beyond it: cascading
// foreign keys of other tables, and triggers.
func (i Introspector) writeEffects(ctx context.Context, schema, table string) ([]entity.Cascade, bool, error) {
	rows, err := i.db.QueryContext(ctx,
		`SELECT r.constraint_name, r.constraint_schema, r.table_name, r.delete_rule, r.update_rule,
		        k.referenced_column_name, k.column_name
		 FROM information_schema.referential_constraints r
		 JOIN information_schema.key_column_usage k
		   ON k.constraint_schema = r.constraint_schema AND k.constraint_name = r.constraint_name
		  AND k.table_name = r.table_name
		 WHERE r.unique_constraint_schema = ? AND r.referenced_table_name = ?
		   AND (r.delete_rule IN ('CASCADE', 'SET NULL', 'SET DEFAULT')
		     OR r.update_rule IN ('CASCADE', 'SET NULL', 'SET DEFAULT'))
		 ORDER BY r.constraint_schema, r.table_name, r.constraint_name, k.ordinal_position`, schema, table)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	var cascades []entity.Cascade
	last := ""
	for rows.Next() {
		var name, onDelete, onUpdate, column, foreign string
		var c entity.Cascade
		if err := rows.Scan(&name, &c.Schema, &c.Table, &onDelete, &onUpdate, &column, &foreign); err != nil {
			return nil, false, err
		}
		if id := c.Schema + "." + c.Table + "." + name; id != last {
			last = id
			c.OnDelete, c.OnUpdate = mysqlAction[onDelete], mysqlAction[onUpdate]
			cascades = append(cascades, c)
		}
		cur := &cascades[len(cascades)-1]
		cur.Columns, cur.ForeignColumns = append(cur.Columns, column), append(cur.ForeignColumns, foreign)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	var triggers int
	err = i.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM information_schema.triggers WHERE event_object_schema = ? AND event_object_table = ?`,
		schema, table).Scan(&triggers)
	return cascades, triggers > 0, err
}
