package postgres

import (
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/dialect"
)

// Dialect is the PostgreSQL dialect.
type Dialect struct{}

func (Dialect) Name() string { return "postgres" }
func (Dialect) QuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
func (Dialect) Placeholder(i int) string   { return "$" + itoa(i+1) }
func (Dialect) ExplainSQL(q string) string { return "EXPLAIN (FORMAT JSON) " + q }
func (Dialect) Capabilities() dialect.Capabilities {
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

// itoa converts a non-negative int to its decimal string without strconv, which
// is overkill for placeholder indexes.
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
