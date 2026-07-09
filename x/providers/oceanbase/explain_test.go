package oceanbase

import (
	"testing"

	"github.com/nethinwei/sql-mcp-server/cost"
)

func TestParseOBExplainMySQLShape(t *testing.T) {
	t.Parallel()
	raw := `{"query_block":{"cost_info":{"query_cost":"50.0"},"table":{"access_type":"ALL","rows":500}}}`
	p, err := parseOBExplain([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if p.ScanType != cost.ScanFull || p.EstimatedRows != 500 {
		t.Fatalf("got %+v", p)
	}
}

func TestParseOBExplainOBNode(t *testing.T) {
	t.Parallel()
	raw := `{"op":"TABLE SCAN","cost":200,"rows":1000}`
	p, _ := parseOBExplain([]byte(raw))
	if p.ScanType != cost.ScanFull || p.TotalCost != 200 {
		t.Fatalf("got %+v", p)
	}
}

func TestParseOBExplainPointGet(t *testing.T) {
	t.Parallel()
	raw := `{"op":"PK POINT GET","cost":1,"rows":1}`
	p, _ := parseOBExplain([]byte(raw))
	if p.ScanType != cost.ScanPoint {
		t.Fatalf("ScanType = %v, want ScanPoint", p.ScanType)
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
