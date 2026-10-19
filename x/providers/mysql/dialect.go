package mysql

import (
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/core/reason"
)

// Dialect is the MySQL dialect. EXPLAIN estimates are less reliable than
// PostgreSQL's, so the gate weakens the Estimate layer and leans on
// EnforceCap + max_execution_time + sql_safe_updates.
type Dialect struct{}

func (Dialect) Name() string { return "mysql" }
func (Dialect) QuoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}
func (Dialect) Placeholder(_ int) string { return "?" }
func (Dialect) Capabilities() dialect.Profile {
	return dialect.Profile{
		dialect.Returning: {Strength: dialect.Unsupported, Evidence: dialect.CoreVerified,
			Reason: reason.New(reason.ReturningLastInsertID)},
		dialect.Explain: {Strength: dialect.Enforced, Evidence: dialect.DBVerified,
			Reason: reason.New(reason.ExplainMySQLJSON)},
		dialect.ExplainAccurate: {Strength: dialect.BestEffort, Evidence: dialect.DBVerified,
			Reason: reason.New(reason.ExplainCoarse)},
		dialect.ExplainAnalyze: {Strength: dialect.Unsupported, Evidence: dialect.CoreVerified,
			Reason: reason.New(reason.ExplainNoAnalyze)},
	}
}
