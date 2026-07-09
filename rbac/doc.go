// Package rbac defines authorization with row-level security. An Authorizer is
// consulted before every DML tool execution (invariant I5: RBAC runs before
// the cost gate). RoleAuthorizer checks the role against the entity's RoleAccess,
// projects fields (invariant I6), and injects a row-level filter predicate for
// the role (invariant I7: effective predicate = user_predicate AND role_filter).
//
// Authorization denials are Decision{Allowed:false}, not errors; errors are
// reserved for authorization-system failures, mirroring fino's policy.Policy.
package rbac
