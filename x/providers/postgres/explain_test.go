package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/codegen"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/store"
)

type analyzeTx struct {
	store.FakeTx
	query func(context.Context, string, ...any) (store.Rows, error)
}

func (t *analyzeTx) QueryContext(ctx context.Context, query string, args ...any) (store.Rows, error) {
	return t.query(ctx, query, args...)
}

func TestParsePGExplainSeqScan(t *testing.T) {
	t.Parallel()
	raw := `[{"Plan":{"Node Type":"Seq Scan","Relation Name":"users","Total Cost":123.45,"Plan Rows":100}}]`
	p, _, err := parsePGExplain([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	// PostgreSQL Seq Scan reads the whole heap -> ScanFull.
	if p.ScanType != cost.ScanFull {
		t.Fatalf("ScanType = %v, want ScanFull", p.ScanType)
	}
	if p.TotalCost != 123.45 || p.EstimatedRows != 100 {
		t.Fatalf("got %+v", p)
	}
	if !p.StatsFresh {
		t.Error("StatsFresh should default true before stats probe")
	}
}

func TestParsePGExplainIndexScan(t *testing.T) {
	t.Parallel()
	raw := `[{"Plan":{"Node Type":"Index Only Scan","Total Cost":4.5,"Plan Rows":1}}]`
	p, _, _ := parsePGExplain([]byte(raw))
	if p.ScanType != cost.ScanIndex {
		t.Fatalf("ScanType = %v, want ScanIndex", p.ScanType)
	}
}

func TestParsePGExplainSort(t *testing.T) {
	t.Parallel()
	raw := `[{"Plan":{"Node Type":"Sort","Total Cost":50,"Plan Rows":10,"Plans":[{"Node Type":"Seq Scan","Relation Name":"users"}]}}]`
	p, root, _ := parsePGExplain([]byte(raw))
	if !p.HasSort {
		t.Error("expected HasSort for Sort node")
	}
	if p.ScanType != cost.ScanFull {
		t.Fatalf("nested scan type = %v, want ScanFull", p.ScanType)
	}
	if root.NodeType != "Sort" {
		t.Fatalf("root = %+v", root)
	}
}

func TestParsePGExplainUsesRiskiestNestedScanAndLargestRows(t *testing.T) {
	t.Parallel()
	raw := `[{"Plan":{"Node Type":"Nested Loop","Plan Rows":3,"Plans":[` +
		`{"Node Type":"Index Scan","Relation Name":"small","Plan Rows":3},` +
		`{"Node Type":"Sort","Plan Rows":250,"Plans":[{"Node Type":"Seq Scan","Relation Name":"large","Plan Rows":12000}]}` +
		`]}}]`
	p, root, err := parsePGExplain([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if p.ScanType != cost.ScanFull || p.EstimatedRows != 12000 || !p.HasSort {
		t.Fatalf("plan = %+v", p)
	}
	if got := relationForScan(root, p.ScanType); got != "large" {
		t.Fatalf("stats relation = %q, want large", got)
	}
}

func TestParsePGExplainHashJoinTempTable(t *testing.T) {
	t.Parallel()
	raw := `[{"Plan":{"Node Type":"Hash Join","Plans":[{"Node Type":"Seq Scan"}]}}]`
	p, _, _ := parsePGExplain([]byte(raw))
	if !p.HasTempTable {
		t.Error("expected HasTempTable for Hash Join")
	}
}

func TestParsePGExplainPartitionPruned(t *testing.T) {
	t.Parallel()
	raw := `[{"Plan":{"Node Type":"Append","Subplans Removed":3,"Plans":[{"Node Type":"Seq Scan"}]}}]`
	p, _, _ := parsePGExplain([]byte(raw))
	if !p.PartitionPruned {
		t.Error("expected PartitionPruned when Subplans Removed > 0")
	}
}

func TestParsePGExplainInvalidDegrades(t *testing.T) {
	t.Parallel()
	p, _, err := parsePGExplain([]byte("not json at all"))
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if p.ScanType != cost.ScanUnknown {
		t.Fatalf("ScanType = %v, want ScanUnknown on bad JSON", p.ScanType)
	}
}

func TestParsePGExplainEmpty(t *testing.T) {
	t.Parallel()
	p, _, _ := parsePGExplain([]byte("[]"))
	if p.ScanType != cost.ScanUnknown {
		t.Fatalf("ScanType = %v, want ScanUnknown on empty", p.ScanType)
	}
}

func TestParsePGExplainAnalyzeRecursivelyCountsRowsAndLoops(t *testing.T) {
	t.Parallel()
	raw := `[{"Plan":{"Node Type":"Nested Loop","Plan Rows":2,"Actual Rows":2,"Actual Loops":1,"Plans":[` +
		`{"Node Type":"Index Scan","Plan Rows":2,"Actual Rows":2,"Actual Loops":1},` +
		`{"Node Type":"Seq Scan","Plan Rows":3,"Actual Rows":3,"Actual Loops":2}` +
		`]},"Execution Time":12.5}]`
	p, err := parsePGExplainAnalyze([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if p.ActualRows != 10 {
		t.Fatalf("ActualRows = %d, want 10", p.ActualRows)
	}
	if p.ExecutionTime != 12500*time.Microsecond {
		t.Fatalf("ExecutionTime = %s, want 12.5ms", p.ExecutionTime)
	}
}

func TestParsePGExplainAnalyzeRejectsInvalidPlans(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"not json", "[]"} {
		if _, err := parsePGExplainAnalyze([]byte(raw)); err == nil {
			t.Fatalf("parsePGExplainAnalyze(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestExplainAnalyzeRejectsUnmarkedQuery(t *testing.T) {
	t.Parallel()
	p := &Provider{}
	_, err := p.ExplainAnalyze(context.Background(), codegen.Compiled{SQL: "DELETE FROM users"})
	if !errors.Is(err, ErrAnalyzeRequiresReadOnly) {
		t.Fatalf("error = %v, want ErrAnalyzeRequiresReadOnly", err)
	}
}

func TestExplainAnalyzeUsesReadOnlyTransactionAndRollsBack(t *testing.T) {
	tx := &analyzeTx{query: func(_ context.Context, query string, _ ...any) (store.Rows, error) {
		if query != "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) SELECT 1" {
			t.Fatalf("query = %q", query)
		}
		raw := `[{"Plan":{"Node Type":"Result","Actual Rows":1,"Actual Loops":1},"Execution Time":0.5}]`
		return store.NewFakeRows([]string{"QUERY PLAN"}, []any{raw}), nil
	}}
	beginner := &store.FakeDB{BeginFn: func(_ context.Context, opts *store.TxOptions) (store.Tx, error) {
		if opts == nil || !opts.ReadOnly {
			t.Fatalf("transaction options = %+v, want read-only", opts)
		}
		return tx, nil
	}}
	p := &Provider{analyzeTx: beginner}
	plan, err := p.ExplainAnalyze(context.Background(), codegen.Compiled{SQL: "SELECT 1", ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ActualRows != 1 || !tx.RolledBack || tx.Committed {
		t.Fatalf("plan=%+v rolledBack=%v committed=%v", plan, tx.RolledBack, tx.Committed)
	}
}

func TestExplainAnalyzeReadOnlyViolationIsSamplingErrorAndRollsBack(t *testing.T) {
	want := errors.New("cannot execute INSERT in a read-only transaction")
	tx := &analyzeTx{query: func(context.Context, string, ...any) (store.Rows, error) {
		return nil, want
	}}
	beginner := &store.FakeDB{BeginFn: func(context.Context, *store.TxOptions) (store.Tx, error) {
		return tx, nil
	}}
	p := &Provider{analyzeTx: beginner}
	_, err := p.ExplainAnalyze(context.Background(), codegen.Compiled{
		SQL: "SELECT volatile_writer()", ReadOnly: true,
	})
	if !errors.Is(err, want) || !tx.RolledBack {
		t.Fatalf("error=%v rolledBack=%v", err, tx.RolledBack)
	}
}
