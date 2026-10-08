// Package testdialect provides dialect implementations for core package tests.
// Production code should use x/providers/*/Dialect instead.
package testdialect

import (
	"strconv"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/dialect"
)

// Postgres is a PostgreSQL dialect for tests.
type Postgres struct{}

func (Postgres) Name() string { return "postgres" }
func (Postgres) QuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
func (Postgres) Placeholder(i int) string { return "$" + strconv.Itoa(i+1) }
func (Postgres) Capabilities() dialect.Capabilities {
	return dialect.Capabilities{Returning: true, ExplainCost: true, ExplainAccurate: true}
}

// MySQL is a MySQL dialect for tests.
type MySQL struct{}

func (MySQL) Name() string { return "mysql" }
func (MySQL) QuoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}
func (MySQL) Placeholder(_ int) string { return "?" }
func (MySQL) Capabilities() dialect.Capabilities {
	return dialect.Capabilities{ExplainCost: true}
}
