package mysql

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"slices"
	"sync"
	"testing"
	"time"
)

// fakeDriver records the statements its connections run; "slow" runs until
// its context ends.
type fakeDriver struct {
	mu   sync.Mutex
	ran  []string
	next int64
}

func (d *fakeDriver) Connect(context.Context) (driver.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.next++
	return &fakeConn{d: d, id: d.next}, nil
}

func (d *fakeDriver) Driver() driver.Driver { return nil }

func (d *fakeDriver) statements() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.ran)
}

type fakeConn struct {
	d  *fakeDriver
	id int64
}

func (c *fakeConn) ExecContext(ctx context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.d.mu.Lock()
	c.d.ran = append(c.d.ran, query)
	c.d.mu.Unlock()
	if query == "slow" {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return driver.RowsAffected(0), nil
}

func (c *fakeConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &idRows{id: c.id}, nil
}

func (c *fakeConn) Prepare(string) (driver.Stmt, error)      { return nil, errors.New("not supported") }
func (c *fakeConn) Close() error                             { return nil }
func (c *fakeConn) Begin() (driver.Tx, error)                { return nil, errors.New("not supported") }
func (c *fakeConn) ResetSession(context.Context) error       { return nil }
func (c *fakeConn) IsValid() bool                            { return true }
func (c *fakeConn) CheckNamedValue(*driver.NamedValue) error { return nil }

// idRows is the one row of SELECT CONNECTION_ID().
type idRows struct {
	id   int64
	done bool
}

func (r *idRows) Columns() []string { return []string{"id"} }
func (r *idRows) Close() error      { return nil }
func (r *idRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done, dest[0] = true, r.id
	return nil
}

func TestKillConnStopsOnlyAStatementPastItsContext(t *testing.T) {
	t.Parallel()
	d := &fakeDriver{}
	connector := newKillConnector(d)
	defer connector.Close()
	raw, err := connector.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c := raw.(*killConn)
	if _, err := c.ExecContext(context.Background(), "fast", nil); err != nil {
		t.Fatal(err)
	}
	if !c.IsValid() || slices.Contains(d.statements(), "KILL QUERY 1") {
		t.Fatalf("a statement that ended in time was killed: %v", d.statements())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.ExecContext(ctx, "slow", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("slow statement: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !slices.Contains(d.statements(), "KILL QUERY 1") {
		if time.Now().After(deadline) {
			t.Fatalf("no KILL QUERY for thread 1: %v", d.statements())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if c.IsValid() || !errors.Is(c.ResetSession(context.Background()), driver.ErrBadConn) {
		t.Fatal("a connection sent a KILL QUERY must not be reused")
	}
}

// A statement that returns as its context ends (as the driver does: it
// closes its side and returns) is still stopped on the server. Context
// cancellation closes Done before it starts the KILL QUERY, so the
// statement's return can come first; it must not call the KILL off.
func TestKillConnStopsAStatementReturningAtItsDeadline(t *testing.T) {
	t.Parallel()
	for i := range 200 {
		d := &fakeDriver{}
		connector := newKillConnector(d)
		raw, err := connector.Connect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		c := raw.(*killConn)
		ctx, cancel := context.WithCancel(context.Background())
		go cancel()
		_, _ = c.ExecContext(ctx, "slow", nil)
		if c.IsValid() {
			t.Fatalf("run %d: a statement past its context left its connection usable", i)
		}
		deadline := time.Now().Add(2 * time.Second)
		for !slices.Contains(d.statements(), "KILL QUERY 1") {
			if time.Now().After(deadline) {
				t.Fatalf("run %d: no KILL QUERY for a statement returning at its deadline: %v", i, d.statements())
			}
			time.Sleep(time.Millisecond)
		}
		_ = connector.Close()
	}
}
