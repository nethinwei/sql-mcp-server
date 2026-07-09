// Package relalg defines a dialect-neutral relational algebra intermediate
// representation (IR). DML tools construct Expr trees (logical plans); the
// codegen package renders them into dialect-specific SQL.
//
// The IR is relationally complete for safe read queries (selection, projection,
// aggregation, sort, limit, distinct) and DML (insert/update/delete over a
// predicate). Join and union are intentionally absent until needed (P1) — the
// surface stays deterministic, inject-proof, and gateable, excluding NL2SQL.
//
// Expr and Predicate are sealed interfaces: their marker methods (rel, pred)
// are unexported, so external packages cannot add nodes. This keeps the
// algebra closed and the codegen exhaustively matchable.
package relalg
