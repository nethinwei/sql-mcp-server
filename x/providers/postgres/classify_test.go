package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nethinwei/sql-mcp-server/core/store"
)

func TestDetailColumns(t *testing.T) {
	t.Parallel()
	for detail, want := range map[string][]string{
		`Key (email)=(a@x) already exists.`:                          {"email"},
		`Key (tenant_id, "Code")=(1, x) already exists.`:             {"tenant_id", "Code"},
		`Key (customer_id)=(5) is not present in table "customers".`: {"customer_id"},
		`Failing row contains (1, null).`:                            nil,
	} {
		if got := detailColumns(detail); !slices.Equal(got, want) {
			t.Errorf("%q: %v, want %v", detail, got, want)
		}
	}
}

// A table or a column the database no longer has is a schema change, and a
// cached plan its table outgrew is stale; other feature_not_supported errors
// are neither.
func TestClassifySchemaChanges(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		code, message string
		want          error
	}{
		{"42P01", `relation "a" does not exist`, store.ErrMissingRelation},
		{"42703", `column "w" does not exist`, store.ErrMissingColumn},
		{"0A000", "cached plan must not change result type", store.ErrStalePlan},
	} {
		if err := classify(&pgconn.PgError{Code: c.code, Message: c.message}); !errors.Is(err, c.want) {
			t.Errorf("%s: %v", c.code, err)
		}
	}
	other := &pgconn.PgError{Code: "0A000", Message: "FOR UPDATE is not allowed with aggregate functions"}
	if got := classify(other); got != error(other) {
		t.Errorf("other errors must pass through: %v", got)
	}
}

// A server that ends the session while a statement runs (an administrator's
// termination, a shutdown or crash, a broken connection) leaves the
// statement's outcome unknown, as a connection the driver lost does.
func TestClassifyTerminatedSessions(t *testing.T) {
	t.Parallel()
	for _, code := range []string{"57P01", "57P02", "08006", "08003", "08000", "08P01"} {
		if err := classify(&pgconn.PgError{Code: code}); !errors.Is(err, store.ErrConnectionLost) {
			t.Errorf("%s: %v, want store.ErrConnectionLost", code, err)
		}
	}
	if err := classify(&pgconn.PgError{Code: "57014"}); !errors.Is(err, store.ErrStatementTimeout) ||
		errors.Is(err, store.ErrConnectionLost) {
		t.Errorf("57014: %v, want a timeout only", err)
	}
}

// A connection that could not be made sent no statement, whatever the
// database answered while connecting (57P03 when it is starting up) or the
// network did: its statement did not run.
func TestClassifyConnectionsNotMade(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("pgx", "postgres://u:p@127.0.0.1:1/db?sslmode=disable&connect_timeout=2")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	_, err = db.ExecContext(context.Background(), "DELETE FROM t")
	if err == nil {
		t.Fatal("connected to a closed port")
	}
	if got := classify(err); !errors.Is(got, store.ErrNotConnected) || errors.Is(got, store.ErrConnectionLost) {
		t.Errorf("connect failure: %v, want store.ErrNotConnected", got)
	}
	refused := fmt.Errorf("%w: PostgreSQL 10.23", ErrUnsupportedServer)
	if got := classify(refused); !errors.Is(got, store.ErrNotConnected) {
		t.Errorf("unsupported server: %v, want store.ErrNotConnected", got)
	}
	if err := classify(&pgconn.PgError{Code: "57P03"}); errors.Is(err, store.ErrConnectionLost) {
		t.Errorf("57P03: %v, which only a connection being made gets", err)
	}
}
