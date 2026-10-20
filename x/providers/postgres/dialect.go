package postgres

import (
	"strconv"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/core/reason"
)

// Dialect is the PostgreSQL dialect.
type Dialect struct{}

func (Dialect) Name() string { return "postgres" }
func (Dialect) QuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
func (Dialect) Placeholder(i int) string { return "$" + strconv.Itoa(i+1) }

// OrdersNullsLast implements dialect.NullsLast.
func (Dialect) OrdersNullsLast() {}
func (Dialect) Capabilities() dialect.Profile {
	return dialect.Profile{
		dialect.Returning: {Strength: dialect.Enforced, Evidence: dialect.CoreVerified,
			Reason: reason.New(reason.ReturningInsert)},
		dialect.Explain: {Strength: dialect.Enforced, Evidence: dialect.DBVerified,
			Reason: reason.New(reason.ExplainPostgresJSON)},
		dialect.ExplainAccurate: {Strength: dialect.Enforced, Evidence: dialect.DBVerified,
			Reason: reason.New(reason.ExplainPlanner)},
		dialect.ExplainAnalyze: {Strength: dialect.Enforced, Evidence: dialect.DBVerified,
			Reason: reason.New(reason.ExplainAnalyze)},
	}
}
