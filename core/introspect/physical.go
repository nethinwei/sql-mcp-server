package introspect

import (
	"context"
	"strings"
)

// Physical says where a connection's relations live, so assembly can tell
// when two entities, possibly configured on different datasources, expose
// the same relation.
type Physical struct {
	// Server identifies the database server; "" when unknown.
	Server string
	// Catalog is the database holding the schemas (PostgreSQL); "" where a
	// schema is a database (MySQL).
	Catalog string
	// FoldCase reports that schema and table names compare
	// case-insensitively (MySQL lower_case_table_names).
	FoldCase bool
}

// PhysicalNamer is implemented by introspectors that can identify their
// server and how it compares names.
type PhysicalNamer interface {
	Physical(ctx context.Context) (Physical, error)
}

// RelationKey identifies schema.table on this server; schema must already be
// resolved (not empty for the default).
func (p Physical) RelationKey(schema, table string) string {
	if p.FoldCase {
		schema, table = strings.ToLower(schema), strings.ToLower(table)
	}
	return p.Server + "\x00" + p.Catalog + "\x00" + schema + "\x00" + table
}
