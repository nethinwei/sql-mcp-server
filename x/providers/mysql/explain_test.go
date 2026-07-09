package mysql

import (
	"testing"

	"github.com/nethinwei/sql-mcp-server/cost"
)

func TestParseMySQLExplainFullScan(t *testing.T) {
	t.Parallel()
	raw := `{"query_block":{"cost_info":{"query_cost":"123.45"},"table":{"access_type":"ALL","rows":100,"using_filesort":false}}}`
	p, err := parseMySQLExplain([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if p.ScanType != cost.ScanFull {
		t.Fatalf("ScanType = %v, want ScanFull", p.ScanType)
	}
	if p.TotalCost != 123.45 || p.EstimatedRows != 100 {
		t.Fatalf("got %+v", p)
	}
}

func TestParseMySQLExplainConstPoint(t *testing.T) {
	t.Parallel()
	raw := `{"query_block":{"cost_info":{"query_cost":"1.0"},"table":{"access_type":"const","rows":1}}}`
	p, _ := parseMySQLExplain([]byte(raw))
	if p.ScanType != cost.ScanPoint {
		t.Fatalf("ScanType = %v, want ScanPoint", p.ScanType)
	}
}

func TestParseMySQLExplainInvalidDegrades(t *testing.T) {
	t.Parallel()
	p, err := parseMySQLExplain([]byte("not json"))
	if err != nil {
		t.Fatal(err)
	}
	if p.ScanType != cost.ScanUnknown {
		t.Fatalf("ScanType = %v, want ScanUnknown", p.ScanType)
	}
}
