// Package entity defines the entity abstraction layer: configured tables,
// views, and procedures exposed to MCP tools, with aliases, field projection,
// masking, descriptions, role permissions, row-level security, and keys.
//
// An Entity is a complete description of a Relation (Codd): attributes with
// domains and constraints, candidate keys, foreign keys, and role access.
// Registry holds an immutable set of entities keyed by logical name. RowPolicies
// attach a relalg.Predicate per role for row-level security, applied by rbac.
package entity
