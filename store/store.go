package store

import (
	"context"
	"iter"
)

// Rows is a forward-only cursor over query results. Callers must Close it,
// typically with defer. It mirrors the subset of *sql.Rows used by core tools.
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Columns() ([]string, error)
	Close() error
	Err() error
}

// Result summarizes a write execution.
type Result struct {
	LastInsertID int64
	RowsAffected int64
}

// DB is the minimal database interface core tools depend on. *sql.DB satisfies
// it via a thin adapter in x/providers; tests use hand-written fakes.
type DB interface {
	QueryContext(ctx context.Context, query string, args ...any) (Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (Result, error)
}

// IsolationLevel names a transaction isolation level.
type IsolationLevel uint8

const (
	// LevelReadUncommitted allows dirty reads.
	LevelReadUncommitted IsolationLevel = iota
	// LevelReadCommitted is the default for most databases.
	LevelReadCommitted
	// LevelRepeatableRead prevents non-repeatable reads.
	LevelRepeatableRead
	// LevelSerializable is the strictest isolation.
	LevelSerializable
)

// TxOptions configures a transaction.
type TxOptions struct {
	Isolation IsolationLevel
	ReadOnly  bool
}

// Savepoint names a savepoint within a transaction.
type Savepoint struct{ Name string }

// Tx is a transaction boundary. ACID is an inseparable part of the relational
// model; tools may execute multiple writes within a Tx. Every Tx must end in
// Commit or Rollback (invariant I10).
type Tx interface {
	QueryContext(ctx context.Context, query string, args ...any) (Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (Result, error)
	Commit() error
	Rollback() error
	Savepoint(ctx context.Context, name string) (Savepoint, error)
	RollbackTo(ctx context.Context, sp Savepoint) error
}

// TxBeginner begins a transaction. *sql.DB satisfies it via an adapter.
type TxBeginner interface {
	BeginTx(ctx context.Context, opts *TxOptions) (Tx, error)
}

// Canceler optionally cancels an in-flight query on the database side when a
// context is cancelled (pg_cancel_backend / KILL QUERY). Providers implement
// it to release DB resources promptly (invariant I11).
type Canceler interface {
	CancelQuery(ctx context.Context, connID int64) error
}

// Row is a single decoded result row keyed by column name.
type Row = map[string]any

// Iter returns a sequence yielding decoded rows and a terminal error. It closes
// the Rows when iteration ends — whether by exhausting the cursor, an early
// break, or a terminal error — so callers need not close it themselves. Use it
// with range over func to stream large result sets without materializing them.
func Iter(rows Rows) iter.Seq2[Row, error] {
	return func(yield func(Row, error) bool) {
		defer func() { _ = rows.Close() }()
		cols, err := rows.Columns()
		if err != nil {
			yield(nil, err)
			return
		}
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				yield(nil, err)
				return
			}
			row := make(Row, len(cols))
			for i, c := range cols {
				row[c] = vals[i]
			}
			if !yield(row, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(nil, err)
		}
	}
}
