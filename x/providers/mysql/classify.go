package mysql

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	mysqldriver "github.com/go-sql-driver/mysql"

	"github.com/nethinwei/sql-mcp-server/core/store"
)

var (
	duplicateKey  = regexp.MustCompile(`for key '([^']*)'`)
	nullColumn    = regexp.MustCompile(`^Column '([^']*)' cannot be null`)
	childColumns  = regexp.MustCompile(`FOREIGN KEY \(([^)]*)\)`)
	parentColumns = regexp.MustCompile("REFERENCES `[^`]*` " + `\(([^)]*)\)`)
)

// refusals are errors for statements the account may not run: a missing
// table, column, database or routine privilege, or a read-only server.
var refusals = map[uint16]bool{1142: true, 1143: true, 1044: true, 1370: true, 1290: true}

// timeouts are the errors of a statement its server-side timeout stopped:
// MySQL's max_execution_time and OceanBase's ob_query_timeout.
var timeouts = map[uint16]bool{3024: true, 4012: true}

// schemaChanges are the errors of a statement naming a table (1146) or a
// column (1054) the database does not have; OceanBase reports the same.
var schemaChanges = map[uint16]error{1146: store.ErrMissingRelation, 1054: store.ErrMissingColumn}

// classify turns an integrity violation into a *store.ConstraintError, a
// refusal into store.ErrPermissionDenied, a statement its server-side timeout
// stopped into store.ErrStatementTimeout, and a connection the driver found broken in the middle of a
// command (ErrInvalidConn; one broken before it is driver.ErrBadConn) into
// store.ErrConnectionLost.
func classify(err error) error {
	if errors.Is(err, mysqldriver.ErrInvalidConn) {
		return fmt.Errorf("%w: %w", store.ErrConnectionLost, err)
	}
	var myErr *mysqldriver.MySQLError
	if !errors.As(err, &myErr) {
		return err
	}
	if timeouts[myErr.Number] {
		return fmt.Errorf("%w: %w", store.ErrStatementTimeout, err)
	}
	if refusals[myErr.Number] {
		return fmt.Errorf("%w: %w", store.ErrPermissionDenied, err)
	}
	if missing := schemaChanges[myErr.Number]; missing != nil {
		return fmt.Errorf("%w: %w", missing, err)
	}
	ce := &store.ConstraintError{Err: err}
	switch myErr.Number {
	case 1062:
		ce.Kind = store.ConstraintUnique
		if m := duplicateKey.FindStringSubmatch(myErr.Message); m != nil {
			// MySQL 8 names the key "table.key".
			ce.Constraint = m[1][strings.LastIndexByte(m[1], '.')+1:]
		}
	case 1048:
		ce.Kind = store.ConstraintNotNull
		if m := nullColumn.FindStringSubmatch(myErr.Message); m != nil {
			ce.Columns = []string{m[1]}
		}
	case 1452: // the child row references a missing parent
		ce.Kind, ce.Columns = store.ConstraintForeignKey, quotedList(childColumns, myErr.Message)
	case 1451: // the parent row is still referenced
		ce.Kind, ce.Columns = store.ConstraintForeignKey, quotedList(parentColumns, myErr.Message)
	case 3819:
		ce.Kind = store.ConstraintCheck
	default:
		return err
	}
	return ce
}

// quotedList reads the `a`, `b` column list re captures from message.
func quotedList(re *regexp.Regexp, message string) []string {
	m := re.FindStringSubmatch(message)
	if m == nil {
		return nil
	}
	columns := strings.Split(m[1], ", ")
	for i, c := range columns {
		columns[i] = strings.Trim(c, "`")
	}
	return columns
}
