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
	other := &mysqldriver.MySQLError{Number: 1064, Message: "You have an error in your SQL syntax"}
	if got := classify(other); got != error(other) {
		t.Errorf("other errors must pass through: %v", got)
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
