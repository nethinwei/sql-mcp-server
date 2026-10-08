// Package codegen renders a relalg.Expr (logical plan) into dialect-specific,
// fully parameterized SQL. User values always bind to placeholders — never
// string-interpolated — so generated SQL is inject-proof (invariant I3).
//
// A Renderer holds a dialect.Dialect. Compile produces a Compiled value
// carrying the SQL, args, read-only flag, affected tables, and (when the
// caller supplies identity keys via WithPrimaryKey or WithIdentityKeys) an
// IsKeyPoint flag for the cost gate's whitelist and write guard.
package codegen
