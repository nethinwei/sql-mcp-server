package hologres

import (
	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

// Dialect is the Hologres dialect. Hologres speaks the PostgreSQL wire
// protocol (server lineage 11), so quoting and placeholders match PostgreSQL.
// The capability profile is deliberately weaker: text-only EXPLAIN, no
// transactional DML, unverified RETURNING. The gate therefore assembles no
// Estimate layer and leans on EnforceCap + statement_timeout, mirroring the
// MySQL posture (see core/cost.NewGateFromCapabilities).
type Dialect struct{}

func (Dialect) Name() string { return "hologres" }
func (Dialect) QuoteIdent(name string) string {
	return postgres.Dialect{}.QuoteIdent(name)
}
func (Dialect) Placeholder(i int) string   { return postgres.Dialect{}.Placeholder(i) }
func (Dialect) ExplainSQL(q string) string { return "EXPLAIN " + q }

func (Dialect) Capabilities() dialect.Capabilities {
	return dialect.Capabilities{
		Returning:    false, // INSERT ... RETURNING support unverified on Hologres
		Savepoint:    false, // transactions cover DDL only; no savepoint semantics for DML
		KeysetCursor: true,
		// Hologres transactions do not make data statements atomic, so the
		// transaction tools must fail closed for this dialect.
		Transaction: false,
		// EXPLAIN is text-only here and its estimates are unverified against
		// Hologres' distributed engine, so no layer may rely on them.
		ExplainJSON:     false,
		ExplainCost:     false,
		ExplainAccurate: false,
		// statement_timeout GUC (milliseconds) injected per connection.
		StatementTimeout: true,
		ScanRowCap:       false,
		SQLSafeUpdates:   false,
		ResourceManager:  false,
	}
}
