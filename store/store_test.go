package store

import (
	"context"
	"errors"
	"testing"
)

func TestIterYieldsAllRows(t *testing.T) {
	rows := NewFakeRows(
		[]string{"id", "name"},
		[]any{int64(1), "alice"},
		[]any{int64(2), "bob"},
	)
	var got []Row
	for row, err := range Iter(rows) {
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

func TestIterClosesOnEarlyBreak(t *testing.T) {
	rows := NewFakeRows([]string{"id"},
		[]any{int64(1)}, []any{int64(2)}, []any{int64(3)})
	for range Iter(rows) {
		break
	}
	if !rows.Closed() {
		t.Fatal("rows not closed on early break")
	}
}

func TestIterPropagatesErr(t *testing.T) {
	rows := NewFakeRows([]string{"id"}, []any{int64(1)}, []any{int64(2)})
	rows.SetErr(errors.New("boom"))
	var sawErr bool
	for _, err := range Iter(rows) {
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
	rows := NewFakeRows([]string{"id"}, []any{int64(1)})
	var v any
	if err := rows.Scan(&v); err != ErrScanBeforeNext {
		t.Fatalf("got %v, want ErrScanBeforeNext", err)
	}
}

func TestFakeRowsScanValues(t *testing.T) {
	t.Parallel()
	r := NewFakeRows([]string{"id", "name"}, []any{int64(1), "alice"})
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
	tx := &FakeTx{}
	sp, err := tx.Savepoint(context.Background(), "s1")
	if err != nil || sp.Name != "s1" {
		t.Fatalf("savepoint: %v %v", sp, err)
	}
	if err := tx.RollbackTo(context.Background(), sp); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil || !tx.Committed {
		t.Fatal("commit failed")
	}
}

func TestFakeDBLogsCalls(t *testing.T) {
	t.Parallel()
	db := &FakeDB{
		QueryFn: func(_ context.Context, _ string, _ ...any) (Rows, error) {
			return NewFakeRows([]string{"id"}, []any{int64(1)}), nil
		},
		ExecFn: func(_ context.Context, _ string, _ ...any) (Result, error) {
			return Result{RowsAffected: 5}, nil
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
