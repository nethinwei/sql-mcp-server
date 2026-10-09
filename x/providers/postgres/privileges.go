package postgres

import (
	"context"
	"database/sql"

	"github.com/nethinwei/sql-mcp-server/core/introspect"
)

// ReadOnly implements introspect.PrivilegeInspector: a hot standby, or a
// session whose transactions default to read-only.
func (i pgIntrospector) ReadOnly(ctx context.Context) (bool, error) {
	var readOnly bool
	err := i.db.QueryRowContext(ctx,
		`SELECT pg_is_in_recovery() OR current_setting('default_transaction_read_only') = 'on'`).Scan(&readOnly)
	return readOnly, err
}

// TablePrivileges implements introspect.PrivilegeInspector: table-level
// privileges of tables in one query, and the column-level ones of tables
// lacking some table-level privilege in a second.
func (i pgIntrospector) TablePrivileges(
	ctx context.Context, schema string, tables []string,
) (map[string]introspect.TablePrivileges, error) {
	out := map[string]introspect.TablePrivileges{}
	var partial []string
	err := i.each(ctx,
		`SELECT c.relname, has_table_privilege(c.oid, 'SELECT'), has_table_privilege(c.oid, 'INSERT'),
		        has_table_privilege(c.oid, 'UPDATE'), has_table_privilege(c.oid, 'DELETE')
		 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = COALESCE(NULLIF($2, ''), current_schema()) AND c.relname = ANY($1)`,
		tables, func(rows *sql.Rows) error {
			var table string
			var sel, ins, upd, del bool
			if err := rows.Scan(&table, &sel, &ins, &upd, &del); err != nil {
				return err
			}
			out[table] = introspect.TablePrivileges{
				Select: introspect.GrantedIf(sel), Insert: introspect.GrantedIf(ins),
				Update: introspect.GrantedIf(upd), Delete: introspect.GrantedIf(del),
			}
			if !sel || !ins || !upd {
				partial = append(partial, table)
			}
			return nil
		}, schema)
	if err != nil || len(partial) == 0 {
		return out, err
	}
	return out, i.columnPrivileges(ctx, schema, partial, out)
}

// columnPrivileges adds the columns granted where the table-level privilege
// is not.
func (i pgIntrospector) columnPrivileges(
	ctx context.Context, schema string, tables []string, out map[string]introspect.TablePrivileges,
) error {
	return i.each(ctx,
		`SELECT c.relname, a.attname, has_column_privilege(a.attrelid, a.attnum, 'SELECT'),
		        has_column_privilege(a.attrelid, a.attnum, 'INSERT'), has_column_privilege(a.attrelid, a.attnum, 'UPDATE')
		 FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = COALESCE(NULLIF($2, ''), current_schema()) AND c.relname = ANY($1)
		   AND a.attnum > 0 AND NOT a.attisdropped
		 ORDER BY c.relname, a.attnum`,
		tables, func(rows *sql.Rows) error {
			var table, column string
			var cs, ci, cu bool
			if err := rows.Scan(&table, &column, &cs, &ci, &cu); err != nil {
				return err
			}
			p := out[table]
			if p.Columns == nil {
				p.Columns = map[string][]string{}
			}
			for action, granted := range map[string]bool{
				"select": cs && p.Select != introspect.PrivilegeGranted,
				"insert": ci && p.Insert != introspect.PrivilegeGranted,
				"update": cu && p.Update != introspect.PrivilegeGranted,
			} {
				if granted {
					p.Columns[action] = append(p.Columns[action], column)
				}
			}
			out[table] = p
			return nil
		}, schema)
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
