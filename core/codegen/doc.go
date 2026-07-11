// Package codegen renders a relalg.Expr (logical plan) into dialect-specific,
// fully parameterized SQL. User values always bind to placeholders — never
// string-interpolated — so generated SQL is inject-proof (invariant I3).
//
// A Renderer holds a dialect.Dialect. Compile produces a Compiled value
// carrying the SQL, args, read-only flag, affected tables, and (when the
// caller supplies primary-key columns via WithPrimaryKey) an IsPKPoint flag
// for the cost gate's whitelist.
package codegen
