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

// classify turns an integrity violation into a *store.ConstraintError and a
// refusal into store.ErrPermissionDenied.
func classify(err error) error {
	var myErr *mysqldriver.MySQLError
	if !errors.As(err, &myErr) {
		return err
	}
	if refusals[myErr.Number] {
		return fmt.Errorf("%w: %w", store.ErrPermissionDenied, err)
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
