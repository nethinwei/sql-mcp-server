// Package dialect abstracts SQL dialect differences: identifier quoting,
// parameter placeholders, EXPLAIN wrapping, and capability negotiation.
//
// Dialect implementations are pure logic and live in this core package. The
// heavier EXPLAIN-result parsing (cost.Explainer) and schema introspection
// (introspect.Introspector) stay in x/providers because they require live
// database access. Capabilities drive both codegen rendering and which Gate
// layers are assembled per database.
package dialect
