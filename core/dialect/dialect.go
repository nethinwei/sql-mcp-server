package dialect

// Dialect abstracts the SQL differences core codegen needs. Implementations are
// stateless and safe for concurrent use.
type Dialect interface {
	Name() string
	QuoteIdent(name string) string
	Placeholder(index int) string
	// Capabilities is what the dialect itself provides, whatever the
	// connection; a connection's Profile adds what depends on how it was
	// opened.
	Capabilities() Profile
}

// NullsLast is a Dialect ordering a term's NULLs last with NULLS LAST, which
// keeps an index on the column serving the order (and is allowed in the
// ORDER BY of a SELECT DISTINCT); codegen orders them last with a CASE on
// any other dialect.
type NullsLast interface {
	Dialect
	OrdersNullsLast()
}
