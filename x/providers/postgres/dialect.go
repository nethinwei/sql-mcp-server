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
func (Dialect) Capabilities() dialect.Profile {
	return dialect.Profile{
		dialect.Returning:       {Strength: dialect.Enforced, Evidence: dialect.CoreVerified, Reason: "INSERT ... RETURNING"},
		dialect.Explain:         {Strength: dialect.Enforced, Evidence: dialect.DBVerified, Reason: "EXPLAIN (FORMAT JSON)"},
		dialect.ExplainAccurate: {Strength: dialect.Enforced, Evidence: dialect.DBVerified, Reason: "planner row estimates"},
		dialect.ExplainAnalyze:  {Strength: dialect.Enforced, Evidence: dialect.DBVerified, Reason: "EXPLAIN ANALYZE"},
	}
}
