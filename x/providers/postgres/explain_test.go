package postgres

import (
	"testing"

	"github.com/nethinwei/sql-mcp-server/cost"
)

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
	// root is Sort; the Seq Scan child is walked and sets ScanFull via root? No:
	// root ScanType is pgScanType("Sort") = ScanUnknown. The walk sets HasSort
	// and the child's Seq Scan sets HasTempTable? No — Seq Scan sets nothing
	// extra. ScanType reflects the root node only.
	if root.NodeType != "Sort" {
		t.Fatalf("root = %+v", root)
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
