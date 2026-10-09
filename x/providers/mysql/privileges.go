package mysql

import (
	"context"
	"database/sql"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/x/providers/sqladapter"
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

// schemaGrants are the privilege types granted to the current account at
// the global and database levels of a schema, and on its tables and columns.
type schemaGrants struct {
	schema  map[string]bool
	tables  map[string]map[string]bool
	columns map[string]map[string][]string // table, privilege, columns
}

// grants reads the grants of the current account on schema ("" for the
// current database) in one query.
func (i Introspector) grants(ctx context.Context, schema string) (schemaGrants, error) {
	g := schemaGrants{schema: map[string]bool{}, tables: map[string]map[string]bool{},
		columns: map[string]map[string][]string{}}
	if schema == "" {
		if err := i.db.QueryRowContext(ctx, `SELECT COALESCE(DATABASE(), '')`).Scan(&schema); err != nil {
			return g, err
		}
	}
	err := sqladapter.Each(ctx, i.db,
		`SELECT '', privilege_type, '' FROM information_schema.user_privileges WHERE grantee = `+grantee+`
		 UNION ALL SELECT '', privilege_type, '' FROM information_schema.schema_privileges
		   WHERE grantee = `+grantee+` AND ? LIKE table_schema
		 UNION ALL SELECT table_name, privilege_type, '' FROM information_schema.table_privileges
		   WHERE grantee = `+grantee+` AND table_schema = ?
		 UNION ALL SELECT table_name, privilege_type, column_name FROM information_schema.column_privileges
		   WHERE grantee = `+grantee+` AND table_schema = ?`,
		[]any{schema, schema, schema}, func(rows *sql.Rows) error {
			var table, privilege, column string
			if err := rows.Scan(&table, &privilege, &column); err != nil {
				return err
			}
			g.add(table, strings.ToLower(privilege), column)
			return nil
		})
	return g, err
}

func (g schemaGrants) add(table, privilege, column string) {
	switch {
	case table == "":
		g.schema[privilege] = true
	case column == "":
		if g.tables[table] == nil {
			g.tables[table] = map[string]bool{}
		}
		g.tables[table][privilege] = true
	default:
		if g.columns[table] == nil {
			g.columns[table] = map[string][]string{}
		}
		g.columns[table][privilege] = append(g.columns[table][privilege], column)
	}
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
func (i Introspector) TablePrivileges(
	ctx context.Context, schema string, tables []string,
) (map[string]introspect.TablePrivileges, error) {
	g, err := i.grants(ctx, schema)
	if err != nil {
		return nil, err
	}
	missing := introspect.PrivilegeDenied
	if i.rolesActive(ctx) {
		missing = introspect.PrivilegeUnknown
	}
	out := make(map[string]introspect.TablePrivileges, len(tables))
	for _, table := range tables {
		out[table] = g.table(table, missing)
	}
	return out, nil
}

func (g schemaGrants) table(table string, missing introspect.Privilege) introspect.TablePrivileges {
	var p introspect.TablePrivileges
	granted := func(privilege string) bool { return g.schema[privilege] || g.tables[table][privilege] }
	of := func(privilege string) introspect.Privilege {
		if granted(privilege) {
			return introspect.PrivilegeGranted
		}
		return missing
	}
	p.Select, p.Insert, p.Update, p.Delete = of("select"), of("insert"), of("update"), of("delete")
	for action, cols := range g.columns[table] {
		if !granted(action) && (action == "select" || action == "insert" || action == "update") {
			if p.Columns == nil {
				p.Columns = map[string][]string{}
			}
			p.Columns[action] = cols
		}
	}
	return p
}

// ProcedurePrivilege implements introspect.PrivilegeInspector. A grant on
// one routine is not listed in information_schema, so without a global or
// database-level EXECUTE the answer is unknown.
func (i Introspector) ProcedurePrivilege(ctx context.Context, schema, _ string) (introspect.Privilege, error) {
	g, err := i.grants(ctx, schema)
	if err != nil || !g.schema["execute"] {
		return introspect.PrivilegeUnknown, err
	}
	return introspect.PrivilegeGranted, nil
}
