package oceanbase

import (
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/cost"
)

func TestParseOBExplainRealShape(t *testing.T) {
	t.Parallel()
	raw := `{"ID":0,"OPERATOR":"TABLE FULL SCAN","NAME":"users","EST.ROWS":2,"EST.TIME(us)":16}`
	p, err := parseOBExplain([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if p.ScanType != cost.ScanFull {
		t.Fatalf("ScanType = %v, want ScanFull", p.ScanType)
	}
	if p.EstimatedRows != 2 || p.TotalCost != 16 {
		t.Fatalf("got rows=%d cost=%v", p.EstimatedRows, p.TotalCost)
	}
}

func TestParseOBExplainTableGet(t *testing.T) {
	t.Parallel()
	raw := `{"OPERATOR":"TABLE GET","NAME":"users","EST.ROWS":1,"EST.TIME(us)":5}`
	p, _ := parseOBExplain([]byte(raw))
	if p.ScanType != cost.ScanPoint {
		t.Fatalf("ScanType = %v, want ScanPoint", p.ScanType)
	}
}

func TestParseOBExplainMySQLShape(t *testing.T) {
	t.Parallel()
	raw := `{"query_block":{"cost_info":{"query_cost":"50.0"},"table":{"access_type":"ALL","rows_examined_per_scan":500}}}`
	p, _ := parseOBExplain([]byte(raw))
	if p.ScanType != cost.ScanFull || p.EstimatedRows != 500 {
		t.Fatalf("got %+v", p)
	}
}

func TestParseOBExplainUnknownDegrades(t *testing.T) {
	t.Parallel()
	p, err := parseOBExplain([]byte("garbage"))
	if err != nil {
		t.Fatal(err)
	}
	if p.ScanType != cost.ScanUnknown {
		t.Fatalf("ScanType = %v, want ScanUnknown", p.ScanType)
	}
}
