package sqladapter

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/store"
)

var errRefused = errors.New("refused")

// refusingConnector opens connections that refuse to prepare a statement or
// begin a transaction.
type refusingConnector struct{}

func (refusingConnector) Connect(context.Context) (driver.Conn, error) { return refusingConn{}, nil }
func (refusingConnector) Driver() driver.Driver                        { return nil }

type refusingConn struct{}

func (refusingConn) Prepare(string) (driver.Stmt, error) { return nil, errRefused }
func (refusingConn) Close() error                        { return nil }
func (refusingConn) Begin() (driver.Tx, error)           { return nil, errRefused }

// Preparing a statement and beginning a transaction classify the driver's
// errors as statements do: a schema change or a refusal met there is acted
// on the same way.
func TestPrepareAndBeginClassifyErrors(t *testing.T) {
	db := sql.OpenDB(refusingConnector{})
	defer db.Close()
	p := New(db, nil, func(err error) error {
		if errors.Is(err, errRefused) {
			return errors.Join(store.ErrPermissionDenied, err)
		}
		return err
	})
	if _, err := p.PrepareContext(context.Background(), "SELECT 1"); !errors.Is(err, store.ErrPermissionDenied) {
		t.Errorf("prepare: %v", err)
	}
	if _, err := p.BeginTx(context.Background(), nil); !errors.Is(err, store.ErrPermissionDenied) {
		t.Errorf("begin: %v", err)
	}
}
