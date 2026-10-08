package postgres

import (
	"strconv"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/dialect"
)

// Dialect is the PostgreSQL dialect.
type Dialect struct{}

func (Dialect) Name() string { return "postgres" }
func (Dialect) QuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
func (Dialect) Placeholder(i int) string { return "$" + strconv.Itoa(i+1) }
func (Dialect) Capabilities() dialect.Capabilities {
	return dialect.Capabilities{Returning: true, ExplainCost: true, ExplainAccurate: true}
}
