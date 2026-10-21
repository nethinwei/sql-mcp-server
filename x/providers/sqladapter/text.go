package sqladapter

import (
	"database/sql"
	"strconv"

	"github.com/nethinwei/sql-mcp-server/core/store"
)

// TextRows is a RowsFunc returning the values of text columns as strings: a
// driver scans some of them (MySQL's text and DECIMAL, PostgreSQL's json,
// jsonb and xml) into []byte, which would serialize to clients as base64.
// The columns of the types in binary (as DatabaseTypeName reports them) stay
// bytes.
func TextRows(binary map[string]bool) RowsFunc { return TypedTextRows(binary, nil) }

// TypedTextRows is TextRows with the values of the column types in unsigned
// read as uint64 however the driver typed them. MySQL's driver reads an
// unsigned BIGINT as uint64 for a statement without arguments (the text
// protocol), but as int64, or as text past int64's range, for one with
// them (the binary protocol): the type of a value, its JSON and the join
// key expand matches it by would depend on whether the request had a filter.
func TypedTextRows(binary, unsigned map[string]bool) RowsFunc {
	return func(rows *sql.Rows) store.Rows { return &textRows{Rows: rows, types: binary, unsigned: unsigned} }
}

// columnKind is how textRows passes on a column's values.
type columnKind uint8

const (
	textColumn columnKind = iota
	binaryColumn
	unsignedColumn
)

type textRows struct {
	*sql.Rows
	types, unsigned map[string]bool
	kinds           []columnKind
}

func (r *textRows) Scan(dest ...any) error {
	if err := r.Rows.Scan(dest...); err != nil {
		return err
	}
	if r.kinds == nil {
		types, err := r.ColumnTypes()
		if err != nil {
			return err
		}
		r.kinds = make([]columnKind, len(types))
		for i, t := range types {
			switch name := t.DatabaseTypeName(); {
			case r.types[name]:
				r.kinds[i] = binaryColumn
			case r.unsigned[name]:
				r.kinds[i] = unsignedColumn
			}
		}
	}
	for i, d := range dest {
		if p, ok := d.(*any); ok && i < len(r.kinds) {
			*p = r.kinds[i].value(*p)
		}
	}
	return nil
}

// value is v, a value of a column of kind k, as textRows passes it on.
func (k columnKind) value(v any) any {
	switch x := v.(type) {
	case []byte:
		if k == binaryColumn {
			return x
		}
		if k == unsignedColumn {
			if u, err := strconv.ParseUint(string(x), 10, 64); err == nil {
				return u
			}
		}
		return string(x)
	case int64:
		if k == unsignedColumn && x >= 0 {
			return uint64(x)
		}
	}
	return v
}
