package mysql

import (
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/dialect"
)

// Dialect is the MySQL dialect. EXPLAIN estimates are less reliable than
// PostgreSQL's, so the gate weakens the Estimate layer and leans on
// EnforceCap + max_execution_time + sql_safe_updates.
type Dialect struct{}

func (Dialect) Name() string { return "mysql" }
func (Dialect) QuoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}
func (Dialect) Placeholder(_ int) string   { return "?" }
func (Dialect) ExplainSQL(q string) string { return "EXPLAIN FORMAT=JSON " + q }
func (Dialect) Capabilities() dialect.Capabilities {
	return dialect.Capabilities{
		Returning:        false,
		Savepoint:        true,
		KeysetCursor:     true,
		ExplainJSON:      true,
		ExplainCost:      true,
		ExplainAccurate:  false,
		StatementTimeout: true,
		ScanRowCap:       true,
		SQLSafeUpdates:   true,
		ResourceManager:  false,
	}
}
