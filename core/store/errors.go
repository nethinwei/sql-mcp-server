package store

import "errors"

// Constraint kinds a ConstraintError reports.
const (
	ConstraintUnique     = "unique"
	ConstraintForeignKey = "foreign_key"
	ConstraintNotNull    = "not_null"
	ConstraintCheck      = "check"
	ConstraintExclusion  = "exclusion"
)

// ConstraintError is a write the database rejected for violating a
// constraint. Providers classify driver errors into it. Constraint and
// Columns serve the gateway only: a client sees the kind and the fields it may
// see, never the constraint name or the database's message.
type ConstraintError struct {
	Kind       string
	Constraint string
	Columns    []string
	Err        error
}

// Error implements error without the driver message.
func (e *ConstraintError) Error() string { return "constraint violation: " + e.Kind }

// Unwrap returns the driver error.
func (e *ConstraintError) Unwrap() error { return e.Err }

// ErrPermissionDenied marks a statement the database refused for the
// connection's account: a missing privilege, or a write on a read-only
// server or transaction. Providers wrap driver errors with it.
var ErrPermissionDenied = errors.New("store: permission denied")
