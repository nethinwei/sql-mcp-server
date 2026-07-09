package postgres

import (
	"testing"

	"github.com/nethinwei/sql-mcp-server/cost"
)

func TestParsePGExplainSeqScan(t *testing.T) {
	t.Parallel()
	raw := `[{"Plan":{"Node Type":"Seq Scan","Relation Name":"users","Total Cost":123.45,"Plan Rows":100}}]`
	p, err := parsePGExplain([]byte(raw))
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
		t.Error("StatsFresh should be true for PG")
	}
}

func TestParsePGExplainIndexScan(t *testing.T) {
	t.Parallel()
	raw := `[{"Plan":{"Node Type":"Index Only Scan","Total Cost":4.5,"Plan Rows":1}}]`
	p, _ := parsePGExplain([]byte(raw))
	if p.ScanType != cost.ScanIndex {
		t.Fatalf("ScanType = %v, want ScanIndex", p.ScanType)
	}
}

func TestParsePGExplainInvalidDegrades(t *testing.T) {
	t.Parallel()
	p, err := parsePGExplain([]byte("not json at all"))
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if p.ScanType != cost.ScanUnknown {
		t.Fatalf("ScanType = %v, want ScanUnknown on bad JSON", p.ScanType)
	}
}

func TestParsePGExplainEmpty(t *testing.T) {
	t.Parallel()
	p, _ := parsePGExplain([]byte("[]"))
	if p.ScanType != cost.ScanUnknown {
		t.Fatalf("ScanType = %v, want ScanUnknown on empty", p.ScanType)
	}
}
