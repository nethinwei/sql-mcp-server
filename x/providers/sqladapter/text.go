package sqladapter

import (
	"database/sql"

	"github.com/nethinwei/sql-mcp-server/core/store"
)

// TextRows is a RowsFunc returning the values of text columns as strings: a
// driver scans some of them (MySQL's text and DECIMAL, PostgreSQL's json,
// jsonb and xml) into []byte, which would serialize to clients as base64.
// The columns of the types in binary (as DatabaseTypeName reports them) stay
// bytes.
func TextRows(binary map[string]bool) RowsFunc {
	return func(rows *sql.Rows) store.Rows { return &textRows{Rows: rows, types: binary} }
}

type textRows struct {
	*sql.Rows
	types  map[string]bool
	binary []bool
}

func (r *textRows) Scan(dest ...any) error {
	if err := r.Rows.Scan(dest...); err != nil {
		return err
	}
	if r.binary == nil {
		types, err := r.ColumnTypes()
		if err != nil {
			return err
		}
		r.binary = make([]bool, len(types))
		for i, t := range types {
			r.binary[i] = r.types[t.DatabaseTypeName()]
		}
	}
	for i, d := range dest {
		if p, ok := d.(*any); ok && i < len(r.binary) && !r.binary[i] {
			if b, ok := (*p).([]byte); ok {
				*p = string(b)
			}
		}
	}
	return nil
}
