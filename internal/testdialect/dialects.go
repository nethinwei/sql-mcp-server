// Package testdialect provides dialect implementations for core package tests.
// Production code should use x/providers/*/Dialect instead.
package testdialect

import (
	"strings"

	"github.com/nethinwei/sql-mcp-server/dialect"
)

// Postgres is a PostgreSQL dialect for tests.
type Postgres struct{}

func (Postgres) Name() string { return "postgres" }
func (Postgres) QuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
func (Postgres) Placeholder(i int) string   { return "$" + itoa(i+1) }
func (Postgres) ExplainSQL(q string) string { return "EXPLAIN (FORMAT JSON) " + q }
func (Postgres) Capabilities() dialect.Capabilities {
	return dialect.Capabilities{
		Returning:        true,
		Savepoint:        true,
		KeysetCursor:     true,
		ExplainJSON:      true,
		ExplainCost:      true,
		ExplainAccurate:  true,
		StatementTimeout: true,
		ScanRowCap:       false,
		SQLSafeUpdates:   false,
		ResourceManager:  false,
	}
}

// MySQL is a MySQL dialect for tests.
type MySQL struct{}

func (MySQL) Name() string { return "mysql" }
func (MySQL) QuoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}
func (MySQL) Placeholder(_ int) string   { return "?" }
func (MySQL) ExplainSQL(q string) string { return "EXPLAIN FORMAT=JSON " + q }
func (MySQL) Capabilities() dialect.Capabilities {
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

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
