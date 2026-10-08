package postgres

import (
	"context"

	"github.com/nethinwei/sql-mcp-server/core/introspect"
)

// relationName is a regclass-parseable name for schema.table ("" schema
// resolves through search_path).
func relationName(schema, table string) string {
	name := Dialect{}.QuoteIdent(table)
	if schema != "" {
		name = Dialect{}.QuoteIdent(schema) + "." + name
	}
	return name
}

// ReadOnly implements introspect.PrivilegeInspector: a hot standby, or a
// session whose transactions default to read-only.
func (i pgIntrospector) ReadOnly(ctx context.Context) (bool, error) {
	var readOnly bool
	err := i.db.QueryRowContext(ctx,
		`SELECT pg_is_in_recovery() OR current_setting('default_transaction_read_only') = 'on'`).Scan(&readOnly)
	return readOnly, err
}

// TablePrivileges implements introspect.PrivilegeInspector.
func (i pgIntrospector) TablePrivileges(ctx context.Context, schema, table string) (introspect.TablePrivileges, error) {
	var p introspect.TablePrivileges
	rel := relationName(schema, table)
	var sel, ins, upd, del bool
	if err := i.db.QueryRowContext(ctx,
		`SELECT has_table_privilege($1::regclass, 'SELECT'), has_table_privilege($1::regclass, 'INSERT'),
		        has_table_privilege($1::regclass, 'UPDATE'), has_table_privilege($1::regclass, 'DELETE')`,
		rel).Scan(&sel, &ins, &upd, &del); err != nil {
		return p, err
	}
	p.Select, p.Insert, p.Update, p.Delete =
		introspect.GrantedIf(sel), introspect.GrantedIf(ins), introspect.GrantedIf(upd), introspect.GrantedIf(del)
	if sel && ins && upd {
		return p, nil
	}
	rows, err := i.db.QueryContext(ctx,
		`SELECT a.attname, has_column_privilege(a.attrelid, a.attnum, 'SELECT'),
		        has_column_privilege(a.attrelid, a.attnum, 'INSERT'), has_column_privilege(a.attrelid, a.attnum, 'UPDATE')
		 FROM pg_attribute a WHERE a.attrelid = $1::regclass AND a.attnum > 0 AND NOT a.attisdropped
		 ORDER BY a.attnum`, rel)
	if err != nil {
		return p, err
	}
	defer func() { _ = rows.Close() }()
	p.Columns = map[string][]string{}
	for rows.Next() {
		var column string
		var cs, ci, cu bool
		if err := rows.Scan(&column, &cs, &ci, &cu); err != nil {
			return p, err
		}
		for action, granted := range map[string]bool{"select": cs && !sel, "insert": ci && !ins, "update": cu && !upd} {
			if granted {
				p.Columns[action] = append(p.Columns[action], column)
			}
		}
	}
	return p, rows.Err()
}

// ProcedurePrivilege implements introspect.PrivilegeInspector.
func (i pgIntrospector) ProcedurePrivilege(ctx context.Context, schema, name string) (introspect.Privilege, error) {
	var count int
	var granted bool
	err := i.db.QueryRowContext(ctx,
		`SELECT count(*), COALESCE(bool_and(has_function_privilege(p.oid, 'EXECUTE')), false)
		 FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		 WHERE p.proname = $2 AND n.nspname = COALESCE(NULLIF($1, ''), current_schema())`,
		schema, name).Scan(&count, &granted)
	if err != nil || count != 1 {
		return introspect.PrivilegeUnknown, err
	}
	return introspect.GrantedIf(granted), nil
}
