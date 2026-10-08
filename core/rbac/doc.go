// Package rbac defines authorization with row-level security. An Authorizer is
// consulted before every DML tool execution (invariant I5: RBAC runs before
// the cost gate). GrantAuthorizer resolves the principal's grants (direct user
// grants, top-level role grants and entity-level role configuration), keeps
// only grants that cover every field the request uses, projects fields
// (invariant I6), and injects the effective row filter (invariant I7:
// user_predicate AND (OR of covering grant rows) AND tenant_policy).
//
// Authorization denials are Decision{Allowed:false}, not errors; errors are
// reserved for authorization-system failures.
package rbac
