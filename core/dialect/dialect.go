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
