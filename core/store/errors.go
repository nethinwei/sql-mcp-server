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

// ErrStatementTimeout marks a statement the database stopped at its timeout
// (a server-side statement timeout, or the cancel request of a request whose
// deadline passed): the database rolled it back, so it did not take effect.
// Providers wrap driver errors with it.
var ErrStatementTimeout = errors.New("store: statement stopped at its timeout")

// ErrConnectionLost marks a statement whose connection broke while it ran:
// the database may or may not have carried it out. Providers wrap driver
// errors with it. A broken connection a driver reports before sending the
// statement (driver.ErrBadConn) is not one.
var ErrConnectionLost = errors.New("store: connection lost during the statement")

// ErrMissingRelation and ErrMissingColumn mark a statement naming a table or
// a column the database does not have (any more): its schema changed under a
// running configuration. Providers wrap driver errors with them.
var (
	ErrMissingRelation = errors.New("store: a table the statement names does not exist")
	ErrMissingColumn   = errors.New("store: a column the statement names does not exist")
)

// ErrStalePlan marks a statement whose prepared plan, cached on its
// connection, no longer matches its table after a schema change. The
// database refused it before running it, and dropped the plan: run again,
// it is planned afresh. Providers wrap driver errors with it.
var ErrStalePlan = errors.New("store: the statement's cached plan no longer matches its table")
