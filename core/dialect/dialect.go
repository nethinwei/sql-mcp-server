package dialect

// Capabilities declares what a database supports. It drives codegen rendering
// (RETURNING or a second statement for the inserted key) and whether the cost
// gate assembles the Estimate layer. See the cost gate design.
type Capabilities struct {
	Returning       bool // INSERT ... RETURNING (PG has; MySQL lacks -> exec + last insert id)
	ExplainCost     bool // EXPLAIN yields a numeric cost (SQLite does not)
	ExplainAccurate bool // estimate trustworthiness (PG high; MySQL/OB medium; SQLite low)
}

// Dialect abstracts the SQL differences core codegen needs. Implementations are
// stateless and safe for concurrent use.
type Dialect interface {
	Name() string
	QuoteIdent(name string) string
	Placeholder(index int) string
	Capabilities() Capabilities
}
