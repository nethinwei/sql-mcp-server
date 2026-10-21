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

// healthErrors are the errors of the database's own trouble: out of disk,
// memory or connections, shutting down, a network error between servers,
// an internal or unknown error, a statement interrupted. Others are a
// statement's own (store.ErrRejected).
var healthErrors = map[uint16]bool{
	1021: true, 1037: true, 1038: true, 1040: true, 1041: true, 1053: true, 1105: true, 1114: true, 1135: true,
	1152: true, 1153: true, 1154: true, 1155: true, 1156: true, 1157: true, 1158: true, 1159: true, 1160: true,
	1161: true, 1180: true, 1181: true, 1203: true, 1317: true,
}

// oceanBaseHealthErrors are OceanBase's own errors of its trouble, beside
// MySQL's: out of memory or past the tenant's limit (4013), an internal
// error (4016), the MemStore full under writes (4030).
var oceanBaseHealthErrors = map[uint16]bool{4013: true, 4016: true, 4030: true}

// classifyOceanBase is classify, with OceanBase's errors of its trouble
// counting against its health rather than taken for a statement's own.
func classifyOceanBase(err error) error {
	var myErr *mysqldriver.MySQLError
	if errors.As(err, &myErr) && oceanBaseHealthErrors[myErr.Number] {
		return err
	}
	return classify(err)
}

// invalidValues are the errors of a value its column cannot hold: out of
// range (1264), truncated or incorrect (1265, 1292, 1366, 1367, 1411), too
// long (1406), invalid JSON (3140), division by zero (1365); OceanBase
// reports the same.
var invalidValues = map[uint16]bool{
	1264: true, 1265: true, 1292: true, 1365: true, 1366: true, 1367: true, 1406: true, 1411: true, 3140: true,
}

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
	if invalidValues[myErr.Number] {
		return fmt.Errorf("%w: %w", store.ErrInvalidValue, err)
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
		if healthErrors[myErr.Number] {
			return err
		}
		return fmt.Errorf("%w: %w", store.ErrRejected, err)
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
