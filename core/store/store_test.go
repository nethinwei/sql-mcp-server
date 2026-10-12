package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/core/store/storetest"
)

func TestIterYieldsAllRows(t *testing.T) {
	rows := storetest.NewFakeRows(
		[]string{"id", "name"},
		[]any{int64(1), "alice"},
		[]any{int64(2), "bob"},
	)
	var got []store.Row
	for row, err := range store.Iter(rows) {
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		got = append(got, row)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}
	if got[0]["id"] != int64(1) || got[1]["name"] != "bob" {
		t.Fatalf("unexpected rows: %v", got)
	}
	if !rows.Closed() {
		t.Fatal("rows not closed after full iteration")
	}
}

// IterNamed keys columns by position: two columns the database names alike
// stay apart, and a name count that does not fit the columns is an error.
func TestIterNamed(t *testing.T) {
	rows := storetest.NewFakeRows([]string{"sum", "sum"}, []any{int64(1), int64(2)})
	var got []store.Row
	for row, err := range store.IterNamed(rows, []string{"sum_a", "sum_b"}) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, row)
	}
	if len(got) != 1 || got[0]["sum_a"] != int64(1) || got[0]["sum_b"] != int64(2) {
		t.Fatalf("rows = %v", got)
	}
	mismatch := storetest.NewFakeRows([]string{"a", "b"}, []any{1, 2})
	for _, err := range store.IterNamed(mismatch, []string{"a"}) {
		if err == nil {
			t.Fatal("a name count that does not fit must fail")
		}
	}
	if !mismatch.Closed() {
		t.Fatal("rows not closed after a mismatch")
	}
}

func TestIterClosesOnEarlyBreak(t *testing.T) {
	rows := storetest.NewFakeRows([]string{"id"},
		[]any{int64(1)}, []any{int64(2)}, []any{int64(3)})
	for range store.Iter(rows) {
		break
	}
	if !rows.Closed() {
		t.Fatal("rows not closed on early break")
	}
}

func TestIterPropagatesErr(t *testing.T) {
	rows := storetest.NewFakeRows([]string{"id"}, []any{int64(1)}, []any{int64(2)})
	rows.SetErr(errors.New("boom"))
	var sawErr bool
	for _, err := range store.Iter(rows) {
		if err != nil {
			sawErr = true
			break
		}
	}
	if !sawErr {
		t.Fatal("did not propagate terminal error")
	}
	if !rows.Closed() {
		t.Fatal("rows not closed on error")
	}
}

func TestFakeRowsScanRequiresNext(t *testing.T) {
	rows := storetest.NewFakeRows([]string{"id"}, []any{int64(1)})
	var v any
	if err := rows.Scan(&v); err != storetest.ErrScanBeforeNext {
		t.Fatalf("got %v, want storetest.ErrScanBeforeNext", err)
	}
}

func TestFakeRowsScanValues(t *testing.T) {
	t.Parallel()
	r := storetest.NewFakeRows([]string{"id", "name"}, []any{int64(1), "alice"})
	if !r.Next() {
		t.Fatal("Next false")
	}
	var id, name any
	if err := r.Scan(&id, &name); err != nil {
		t.Fatal(err)
	}
	if id != int64(1) || name != "alice" {
		t.Fatalf("got %v %v", id, name)
	}
	var s string
	if err := r.Scan(&s); err == nil {
		t.Fatal("expected error for non-*any scan target")
	}
}

func TestFakeTx(t *testing.T) {
	t.Parallel()
	tx := &storetest.FakeTx{}
	if err := tx.Commit(); err != nil || !tx.Committed {
		t.Fatal("commit failed")
	}
}

func TestFakeDBBeginTx(t *testing.T) {
	t.Parallel()
	db := &storetest.FakeDB{BeginFn: func(_ context.Context, _ *store.TxOptions) (store.Tx, error) {
		return &storetest.FakeTx{}, nil
	}}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil || tx == nil {
		t.Fatalf("begin: %v %v", tx, err)
	}
}

func TestFakeDBLogsCalls(t *testing.T) {
	t.Parallel()
	db := &storetest.FakeDB{
		QueryFn: func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
			return storetest.NewFakeRows([]string{"id"}, []any{int64(1)}), nil
		},
		ExecFn: func(_ context.Context, _ string, _ ...any) (store.Result, error) {
			return store.Result{RowsAffected: 5}, nil
		},
	}
	rows, err := db.QueryContext(context.Background(), "SELECT 1", int64(42))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	if len(db.Queries) != 1 || db.Queries[0].Query != "SELECT 1" {
		t.Fatalf("query not logged: %v", db.Queries)
	}
	res, err := db.ExecContext(context.Background(), "UPDATE t SET x=1")
	if err != nil {
		t.Fatal(err)
	}
	if res.RowsAffected != 5 {
		t.Fatalf("got %d affected, want 5", res.RowsAffected)
	}
	if len(db.Execs) != 1 {
		t.Fatalf("exec not logged: %v", db.Execs)
	}
}

func TestPreparedDBFallsBackForPlainDB(t *testing.T) {
	db := &storetest.FakeDB{ExecFn: func(context.Context, string, ...any) (store.Result, error) {
		return store.Result{RowsAffected: 1}, nil
	}}
	if _, err := store.WithPreparedCache(db, 2).ExecContext(context.Background(), "update t set x=1"); err != nil {
		t.Fatal(err)
	}
}
