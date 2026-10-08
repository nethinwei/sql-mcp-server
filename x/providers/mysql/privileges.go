package mysql

import (
	"context"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/introspect"
)

// grantee renders CURRENT_USER() the way information_schema names grantees.
const grantee = `CONCAT("'", SUBSTRING_INDEX(CURRENT_USER(), '@', 1), "'@'",
	SUBSTRING_INDEX(CURRENT_USER(), '@', -1), "'")`

// ReadOnly implements introspect.PrivilegeInspector: read_only or
// super_read_only (a replica), which refuse every write of an ordinary
// account.
func (i Introspector) ReadOnly(ctx context.Context) (bool, error) {
	var readOnly bool
	if err := i.db.QueryRowContext(ctx, `SELECT @@global.read_only`).Scan(&readOnly); err != nil {
		return false, err
	}
	var superReadOnly bool
	if err := i.db.QueryRowContext(ctx, `SELECT @@global.super_read_only`).Scan(&superReadOnly); err == nil {
		readOnly = readOnly || superReadOnly // not every MySQL-compatible server has it
	}
	return readOnly, nil
}

// grants lists the privilege types granted to the current account on
// schema.table at the global, database and table levels, and per column.
func (i Introspector) grants(ctx context.Context, schema, table string) (map[string]bool, map[string][]string, error) {
	if schema == "" {
		if err := i.db.QueryRowContext(ctx, `SELECT COALESCE(DATABASE(), '')`).Scan(&schema); err != nil {
			return nil, nil, err
		}
	}
	rows, err := i.db.QueryContext(ctx,
		`SELECT privilege_type, '' FROM information_schema.user_privileges WHERE grantee = `+grantee+`
		 UNION ALL SELECT privilege_type, '' FROM information_schema.schema_privileges
		   WHERE grantee = `+grantee+` AND ? LIKE table_schema
		 UNION ALL SELECT privilege_type, '' FROM information_schema.table_privileges
		   WHERE grantee = `+grantee+` AND table_schema = ? AND table_name = ?
		 UNION ALL SELECT privilege_type, column_name FROM information_schema.column_privileges
		   WHERE grantee = `+grantee+` AND table_schema = ? AND table_name = ?`,
		schema, schema, table, schema, table)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	granted, columns := map[string]bool{}, map[string][]string{}
	for rows.Next() {
		var privilege, column string
		if err := rows.Scan(&privilege, &column); err != nil {
			return nil, nil, err
		}
		privilege = strings.ToLower(privilege)
		if column == "" {
			granted[privilege] = true
		} else {
			columns[privilege] = append(columns[privilege], column)
		}
	}
	return granted, columns, rows.Err()
}

// rolesActive reports roles that may grant privileges information_schema
// does not list; unknown on servers without roles.
func (i Introspector) rolesActive(ctx context.Context) bool {
	var role string
	if err := i.db.QueryRowContext(ctx, `SELECT CURRENT_ROLE()`).Scan(&role); err != nil {
		return true
	}
	return role != "NONE"
}

// TablePrivileges implements introspect.PrivilegeInspector.
func (i Introspector) TablePrivileges(ctx context.Context, schema, table string) (introspect.TablePrivileges, error) {
	var p introspect.TablePrivileges
	granted, columns, err := i.grants(ctx, schema, table)
	if err != nil {
		return p, err
	}
	missing := introspect.PrivilegeDenied
	if i.rolesActive(ctx) {
		missing = introspect.PrivilegeUnknown
	}
	of := func(privilege string) introspect.Privilege {
		if granted[privilege] {
			return introspect.PrivilegeGranted
		}
		return missing
	}
	p.Select, p.Insert, p.Update, p.Delete = of("select"), of("insert"), of("update"), of("delete")
	for action, cols := range columns {
		if !granted[action] && (action == "select" || action == "insert" || action == "update") {
			if p.Columns == nil {
				p.Columns = map[string][]string{}
			}
			p.Columns[action] = cols
		}
	}
	return p, nil
}

// ProcedurePrivilege implements introspect.PrivilegeInspector. A grant on
// one routine is not listed in information_schema, so without a global or
// database-level EXECUTE the answer is unknown.
func (i Introspector) ProcedurePrivilege(ctx context.Context, schema, name string) (introspect.Privilege, error) {
	granted, _, err := i.grants(ctx, schema, name)
	if err != nil || !granted["execute"] {
		return introspect.PrivilegeUnknown, err
	}
	return introspect.PrivilegeGranted, nil
}
