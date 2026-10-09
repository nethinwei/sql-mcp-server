package mysql

import (
	"context"
	"database/sql"

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
