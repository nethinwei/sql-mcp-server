package sqladapter

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/store"
)

var errLost = errors.New("connection lost")

// planConnector opens connections answering every query with lines, then
// failing with fail (io.EOF: the rows end).
type planConnector struct {
	lines []string
	fail  error
}

func (c planConnector) Connect(context.Context) (driver.Conn, error) { return planConn(c), nil }
func (planConnector) Driver() driver.Driver                          { return nil }

type planConn planConnector

func (planConn) Prepare(string) (driver.Stmt, error) { return nil, errRefused }
func (planConn) Close() error                        { return nil }
func (planConn) Begin() (driver.Tx, error)           { return nil, errRefused }
func (c planConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &planRows{lines: c.lines, fail: c.fail}, nil
}

type planRows struct {
	lines []string
	fail  error
}

func (*planRows) Columns() []string { return []string{"plan"} }
func (*planRows) Close() error      { return nil }
func (r *planRows) Next(dest []driver.Value) error {
	if len(r.lines) == 0 {
		return r.fail
	}
	dest[0], r.lines = r.lines[0], r.lines[1:]
	return nil
}

func lostAsUnavailable(err error) error {
	if errors.Is(err, errLost) {
		return errors.Join(store.ErrPermissionDenied, err)
	}
	return err
}

// The plan's rows are joined and the connection is free again when Explain
// returns: the caller queries the pool, of one connection, again.
func TestExplainReadsThePlanAndFreesTheConnection(t *testing.T) {
	db := sql.OpenDB(planConnector{lines: []string{"a", "b"}, fail: io.EOF})
	defer db.Close()
	db.SetMaxOpenConns(1)
	p := New(db, nil, lostAsUnavailable)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := p.Explain(ctx, "EXPLAIN x", nil, "\n")
	if err != nil || got != "a\nb" {
		t.Fatalf("plan = %q, %v", got, err)
	}
	if err := db.QueryRowContext(ctx, "SELECT 1").Scan(new(string)); err != nil {
		t.Fatalf("the pool's one connection is still held: %v", err)
	}
	if empty, err := New(sql.OpenDB(planConnector{fail: io.EOF}), nil, nil).Explain(ctx, "x", nil, ""); err != nil ||
		empty != "" {
		t.Fatalf("no rows = %q, %v", empty, err)
	}
}

// Rows that fail to read are an error, classified, not a plan of unknown
// cost.
func TestExplainClassifiesARowsError(t *testing.T) {
	db := sql.OpenDB(planConnector{lines: []string{"a"}, fail: errLost})
	defer db.Close()
	_, err := New(db, nil, lostAsUnavailable).Explain(context.Background(), "EXPLAIN x", nil, "")
	if !errors.Is(err, store.ErrPermissionDenied) || !errors.Is(err, errLost) {
		t.Fatalf("err = %v", err)
	}
}
