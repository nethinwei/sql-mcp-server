package sqladapter

import (
	"context"
	"strings"
)

// Explain runs an EXPLAIN statement and returns the text of its rows, joined
// by sep ("" when the plan is none: no rows). The rows are read and closed
// before it returns, so the caller may query the pool again for what the
// plan leaves out (a table's size) without holding a second connection.
// An error running the statement or reading its rows is classified: a
// connection lost while reading is no plan, not one of unknown cost.
func (d *Pool) Explain(ctx context.Context, query string, args []any, sep string) (string, error) {
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return "", d.classify(err)
	}
	defer func() { _ = rows.Close() }()
	var sb strings.Builder
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return "", d.classify(err)
		}
		if sb.Len() > 0 {
			sb.WriteString(sep)
		}
		sb.WriteString(s)
	}
	if err := rows.Err(); err != nil {
		return "", d.classify(err)
	}
	return sb.String(), nil
}
