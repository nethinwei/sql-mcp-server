package sqladapter

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"reflect"
	"testing"
)

// protocolConnector answers a statement as MySQL's driver does: without
// arguments by the text protocol, with them by the binary one.
type protocolConnector struct{}

func (protocolConnector) Connect(context.Context) (driver.Conn, error) { return protocolConn{}, nil }
func (protocolConnector) Driver() driver.Driver                        { return nil }

type protocolConn struct{}

func (protocolConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (protocolConn) Close() error                        { return nil }
func (protocolConn) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }

func (protocolConn) QueryContext(_ context.Context, _ string, args []driver.NamedValue) (driver.Rows, error) {
	if len(args) == 0 {
		return &protocolRows{rows: [][]driver.Value{
			{uint64(5), []byte("a"), []byte{1}}, {uint64(1 << 63), []byte("b"), []byte{2}},
		}}, nil
	}
	return &protocolRows{rows: [][]driver.Value{
		{int64(5), []byte("a"), []byte{1}}, {[]byte("9223372036854775808"), []byte("b"), []byte{2}},
	}}, nil
}

type protocolRows struct{ rows [][]driver.Value }

func (r *protocolRows) Columns() []string { return []string{"id", "name", "data"} }
func (r *protocolRows) Close() error      { return nil }

func (r *protocolRows) Next(dest []driver.Value) error {
	if len(r.rows) == 0 {
		return io.EOF
	}
	copy(dest, r.rows[0])
	r.rows = r.rows[1:]
	return nil
}

func (r *protocolRows) ColumnTypeDatabaseTypeName(i int) string {
	return []string{"UNSIGNED BIGINT", "VARCHAR", "BLOB"}[i]
}

// An unsigned BIGINT reads as uint64 whichever protocol the statement took;
// text reads as a string and a blob as bytes.
func TestTypedTextRowsHideTheProtocol(t *testing.T) {
	t.Parallel()
	db := sql.OpenDB(protocolConnector{})
	defer db.Close()
	p := New(db, TypedTextRows(map[string]bool{"BLOB": true}, map[string]bool{"UNSIGNED BIGINT": true}), nil)
	want := [][]any{{uint64(5), "a", []byte{1}}, {uint64(1 << 63), "b", []byte{2}}}
	for _, args := range [][]any{nil, {1}} {
		rows, err := p.QueryContext(context.Background(), "SELECT id, name, data FROM t", args...)
		if err != nil {
			t.Fatal(err)
		}
		var got [][]any
		for rows.Next() {
			row := make([]any, 3)
			if err := rows.Scan(&row[0], &row[1], &row[2]); err != nil {
				t.Fatal(err)
			}
			got = append(got, row)
		}
		_ = rows.Close()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("args %v: rows = %#v, want %#v", args, got, want)
		}
	}
}
