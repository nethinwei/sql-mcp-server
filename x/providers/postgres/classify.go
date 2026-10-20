package postgres

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nethinwei/sql-mcp-server/core/store"
)

// constraintKinds maps integrity-violation SQLSTATEs to constraint kinds.
var constraintKinds = map[string]string{
	"23505": store.ConstraintUnique,
	"23503": store.ConstraintForeignKey,
	"23502": store.ConstraintNotNull,
	"23514": store.ConstraintCheck,
	"23P01": store.ConstraintExclusion,
}

// refusals are SQLSTATEs for statements the account may not run: a missing
// privilege, or a write on a hot standby or in a read-only transaction.
var refusals = map[string]bool{"42501": true, "25006": true}

// schemaChanges are SQLSTATEs of a statement naming a table or a column the
// database does not have.
var schemaChanges = map[string]error{"42P01": store.ErrMissingRelation, "42703": store.ErrMissingColumn}

// stalePlan is the message of the feature_not_supported (0A000) error a
// cached plan whose table changed its result columns gets.
const stalePlan = "cached plan must not change result type"

// queryCanceled is the SQLSTATE of a statement stopped by statement_timeout or
// by the cancel request a cancelled request sends.
const queryCanceled = "57014"

// sessionEnded tells a session the server ended while a statement ran: an
// administrator's termination, a shutdown or crash (57P01-57P03), or a
// broken connection (class 08). The statement may or may not have run.
func sessionEnded(code string) bool {
	return code == "57P01" || code == "57P02" || code == "57P03" || strings.HasPrefix(code, "08")
}

// classify turns an integrity violation into a *store.ConstraintError, a
// refusal into store.ErrPermissionDenied, a cancelled statement into
// store.ErrStatementTimeout, a session the server ended into
// store.ErrConnectionLost, a missing table or column into
// store.ErrMissingRelation or store.ErrMissingColumn, and a stale cached plan
// into store.ErrStalePlan.
func classify(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	if sessionEnded(pgErr.Code) {
		return fmt.Errorf("%w: %w", store.ErrConnectionLost, err)
	}
	if pgErr.Code == queryCanceled {
		return fmt.Errorf("%w: %w", store.ErrStatementTimeout, err)
	}
	if refusals[pgErr.Code] {
		return fmt.Errorf("%w: %w", store.ErrPermissionDenied, err)
	}
	if missing := schemaChanges[pgErr.Code]; missing != nil {
		return fmt.Errorf("%w: %w", missing, err)
	}
	if pgErr.Code == "0A000" && strings.Contains(pgErr.Message, stalePlan) {
		return fmt.Errorf("%w: %w", store.ErrStalePlan, err)
	}
	kind, ok := constraintKinds[pgErr.Code]
	if !ok {
		return err
	}
	columns := detailColumns(pgErr.Detail)
	if pgErr.ColumnName != "" {
		columns = []string{pgErr.ColumnName}
	}
	return &store.ConstraintError{Kind: kind, Constraint: pgErr.ConstraintName, Columns: columns, Err: err}
}

// detailColumns reads the columns of "Key (a, b)=(...) ..." details.
func detailColumns(detail string) []string {
	rest, ok := strings.CutPrefix(detail, "Key (")
	if !ok {
		return nil
	}
	list, _, ok := strings.Cut(rest, ")=")
	if !ok {
		return nil
	}
	columns := strings.Split(list, ", ")
	for i, c := range columns {
		columns[i] = strings.Trim(c, `"`)
	}
	return columns
}
