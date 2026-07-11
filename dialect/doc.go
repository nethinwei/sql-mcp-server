// Package dialect abstracts SQL dialect differences: identifier quoting,
// parameter placeholders, EXPLAIN wrapping, and capability negotiation.
//
// Concrete dialect implementations live in x/providers alongside each database
// adapter. The heavier EXPLAIN-result parsing (cost.Explainer) and schema
// introspection (introspect.Introspector) also stay in x/providers because they
// require live database access. Capabilities drive both codegen rendering and
// which Gate layers are assembled per database.
package dialect
