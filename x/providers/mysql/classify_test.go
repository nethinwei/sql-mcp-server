package mysql

import (
	"database/sql/driver"
	"errors"
	"slices"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql"

	"github.com/nethinwei/sql-mcp-server/core/store"
)

func TestClassifyConstraintViolations(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		number           uint16
		message          string
		kind, constraint string
		columns          []string
	}{
		"duplicate": {1062, "Duplicate entry 'a' for key 'users.users_email_key'", store.ConstraintUnique,
			"users_email_key", nil},
		"not null": {1048, "Column 'email' cannot be null", store.ConstraintNotNull, "", []string{"email"}},
		"missing parent": {1452, "Cannot add or update a child row: a foreign key constraint fails (`db`.`orders`, " +
			"CONSTRAINT `fk` FOREIGN KEY (`tenant_id`, `customer_id`) REFERENCES `customers` (`tenant_id`, `id`))",
			store.ConstraintForeignKey, "", []string{"tenant_id", "customer_id"}},
		"referenced parent": {1451, "Cannot delete or update a parent row: a foreign key constraint fails (`db`.`orders`, " +
			"CONSTRAINT `fk` FOREIGN KEY (`customer_id`) REFERENCES `customers` (`id`))",
			store.ConstraintForeignKey, "", []string{"id"}},
		"check": {3819, "Check constraint 'positive' is violated.", store.ConstraintCheck, "", nil},
	} {
		var ce *store.ConstraintError
		if !errors.As(classify(&mysqldriver.MySQLError{Number: tc.number, Message: tc.message}), &ce) {
			t.Fatalf("%s: not classified", name)
		}
		if ce.Kind != tc.kind || ce.Constraint != tc.constraint || !slices.Equal(ce.Columns, tc.columns) {
			t.Errorf("%s: %+v", name, ce)
		}
	}
	// Another error of the statement is its own, the driver's kept; one of
	// the server's trouble passes through, counting against its health.
	other := &mysqldriver.MySQLError{Number: 1064, Message: "You have an error in your SQL syntax"}
	var ce *store.ConstraintError
	if got := classify(other); errors.As(got, &ce) || !errors.Is(got, store.ErrRejected) || !errors.Is(got, other) {
		t.Errorf("a statement's own error: %v", got)
	}
	server := &mysqldriver.MySQLError{Number: 1040, Message: "Too many connections"}
	if got := classify(server); got != error(server) {
		t.Errorf("the server's trouble must pass through: %v", got)
	}
}

func TestClassifyRefusals(t *testing.T) {
	t.Parallel()
	for _, number := range []uint16{1142, 1143, 1044, 1370, 1290} {
		err := classify(&mysqldriver.MySQLError{Number: number, Message: "denied"})
		if !errors.Is(err, store.ErrPermissionDenied) {
			t.Errorf("%d: %v", number, err)
		}
	}
}

// A connection broken in the middle of a command leaves the command's outcome
// unknown; one found broken before it was sent does not.
func TestClassifyLostConnections(t *testing.T) {
	t.Parallel()
	if err := classify(mysqldriver.ErrInvalidConn); !errors.Is(err, store.ErrConnectionLost) {
		t.Errorf("ErrInvalidConn = %v", err)
	}
	if err := classify(driver.ErrBadConn); errors.Is(err, store.ErrConnectionLost) {
		t.Errorf("ErrBadConn = %v", err)
	}
}

// A table or a column the database no longer has is a schema change.
func TestClassifySchemaChanges(t *testing.T) {
	t.Parallel()
	for number, want := range map[uint16]error{1146: store.ErrMissingRelation, 1054: store.ErrMissingColumn} {
		if err := classify(&mysqldriver.MySQLError{Number: number, Message: "gone"}); !errors.Is(err, want) {
			t.Errorf("%d: %v", number, err)
		}
	}
}

// A value its column cannot hold is the caller's, not the database's
// trouble.
func TestClassifyInvalidValues(t *testing.T) {
	t.Parallel()
	for _, number := range []uint16{1264, 1265, 1292, 1365, 1366, 1367, 1406, 1411, 3140} {
		if err := classify(&mysqldriver.MySQLError{Number: number}); !errors.Is(err, store.ErrInvalidValue) {
			t.Errorf("%d: %v", number, err)
		}
	}
}

// OceanBase's errors of its own trouble count against its health; on
// MySQL the same numbers are a statement's own.
func TestClassifyOceanBaseTrouble(t *testing.T) {
	t.Parallel()
	for _, n := range []uint16{4013, 4016, 4030} {
		err := &mysqldriver.MySQLError{Number: n}
		if got := classifyOceanBase(err); errors.Is(got, store.ErrRejected) {
			t.Errorf("OceanBase %d = %v, rejected", n, got)
		}
		if got := classify(err); !errors.Is(got, store.ErrRejected) {
			t.Errorf("MySQL %d = %v, not rejected", n, got)
		}
	}
	var ce *store.ConstraintError
	if got := classifyOceanBase(&mysqldriver.MySQLError{Number: 1062}); !errors.As(got, &ce) {
		t.Errorf("a duplicate key = %v", got)
	}
}
