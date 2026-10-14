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

// queryCanceled is the SQLSTATE of a statement stopped by statement_timeout or
// by the cancel request a cancelled request sends.
const queryCanceled = "57014"

// classify turns an integrity violation into a *store.ConstraintError, a
// refusal into store.ErrPermissionDenied, and a cancelled statement into a
// context.DeadlineExceeded, as the request's own deadline would have been.
func classify(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	if pgErr.Code == queryCanceled {
		return fmt.Errorf("%w: %w", store.ErrStatementTimeout, err)
	}
	if refusals[pgErr.Code] {
		return fmt.Errorf("%w: %w", store.ErrPermissionDenied, err)
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
