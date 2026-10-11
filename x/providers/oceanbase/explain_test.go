package oceanbase

import (
	"fmt"
	"strings"
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

// An aggregate's scan is a child of its GROUP BY operator; a join's scans
// are children of the join: the plan is as risky as its worst scan.
func TestParseOBExplainNestedScans(t *testing.T) {
	t.Parallel()
	grouped := `{"ID":0,"OPERATOR":"SCALAR GROUP BY","EST.ROWS":1,"EST.TIME(us)":7,` +
		`"CHILD_1":{"ID":1,"OPERATOR":"TABLE RANGE SCAN","NAME":"entries","EST.ROWS":100,"EST.TIME(us)":5}}`
	p, _ := parseOBExplain([]byte(grouped))
	if p.ScanType != cost.ScanIndex || p.EstimatedRows != 100 || p.TotalCost != 7 {
		t.Fatalf("grouped = %+v", p)
	}
	joined := `{"OPERATOR":"NESTED-LOOP JOIN","EST.ROWS":10,"EST.TIME(us)":90,` +
		`"CHILD_1":{"OPERATOR":"SORT","CHILD_1":{"OPERATOR":"TABLE FULL SCAN","EST.ROWS":5000}},` +
		`"CHILD_2":{"OPERATOR":"TABLE GET","EST.ROWS":1}}`
	p, _ = parseOBExplain([]byte(joined))
	if p.ScanType != cost.ScanFull || p.EstimatedRows != 5000 || !p.HasSort {
		t.Fatalf("joined = %+v", p)
	}
}

// A join may estimate far more rows than either of its scans: the plan
// keeps the largest estimate of any operator.
func TestParseOBExplainKeepsJoinEstimate(t *testing.T) {
	t.Parallel()
	joined := `{"OPERATOR":"HASH JOIN","EST.ROWS":1000000,"EST.TIME(us)":900,` +
		`"CHILD_1":{"OPERATOR":"TABLE RANGE SCAN","EST.ROWS":1000},` +
		`"CHILD_2":{"OPERATOR":"TABLE RANGE SCAN","EST.ROWS":1000}}`
	p, _ := parseOBExplain([]byte(joined))
	if p.EstimatedRows != 1000000 || p.ScanType != cost.ScanIndex {
		t.Fatalf("joined = %+v", p)
	}
}

// The size of a full scan's table comes from EXPLAIN EXTENDED's optimization
// info, matched to the scan by its table: an entry of another table (one read
// through an index) never stands in for a full scan's missing one.
func TestOBTableRows(t *testing.T) {
	t.Parallel()
	plan := `|ID|OPERATOR          |NAME |EST.ROWS|EST.TIME(us)|
|0 |HASH JOIN         |     |1       |5           |
|1 ||-TABLE RANGE SCAN|y(kb)|1       |3           |
|2 |+-TABLE FULL SCAN |x    |1       |3           |
Optimization Info:
-------------------------------------
`
	entry := func(name string, rows int) string {
		return fmt.Sprintf("  %s:\n      table_rows:%d\n      output_rows:1\n", name, rows)
	}
	if rows, ok := obTableRows(plan + entry("y", 20) + entry("x", 2000000) + "  Plan Type:\n      LOCAL\n"); !ok ||
		rows != 2000000 {
		t.Fatalf("rows = %d, %v", rows, ok)
	}
	if _, ok := obTableRows(plan + entry("y", 20) + entry("z", 20)); ok {
		t.Fatal("sized with the full scan's table missing from the optimization info")
	}
	join := strings.Replace(plan, "|1 ||-TABLE RANGE SCAN|y(kb)", "|1 ||-TABLE FULL SCAN |y(kb)", 1)
	if rows, ok := obTableRows(join + entry("y", 20) + entry("x", 30)); !ok || rows != 30 {
		t.Fatalf("two full scans: %d, %v", rows, ok)
	}
	if _, ok := obTableRows("|0 |TABLE FULL SCAN|t|"); ok {
		t.Fatal("sized without optimization info")
	}
}

func TestParseOBExplainExpression(t *testing.T) {
	t.Parallel()
	for name, raw := range map[string]string{
		"constant select": `{"ID":0,"OPERATOR":"EXPRESSION","NAME":"","EST.ROWS":1,"EST.TIME(us)":1}`,
		"insert values": `{"ID":0,"OPERATOR":"DISTRIBUTED INSERT","NAME":"","EST.ROWS":1,"EST.TIME(us)":13,` +
			`"CHILD_1":{"ID":1,"OPERATOR":"EXPRESSION","NAME":"","EST.ROWS":1,"EST.TIME(us)":1}}`,
	} {
		p, err := parseOBExplain([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if p.ScanType != cost.ScanPoint || p.EstimatedRows != 1 {
			t.Errorf("%s: %+v, want a point read of one row", name, p)
		}
	}
	// A scan anywhere in the plan outranks the expression.
	raw := `{"ID":0,"OPERATOR":"DISTRIBUTED INSERT","EST.ROWS":2,"CHILD_1":{"ID":1,"OPERATOR":"EXPRESSION",` +
		`"EST.ROWS":2,"CHILD_1":{"ID":2,"OPERATOR":"TABLE FULL SCAN","NAME":"users","EST.ROWS":2}}}`
	if p, _ := parseOBExplain([]byte(raw)); p.ScanType != cost.ScanFull {
		t.Errorf("INSERT ... SELECT: ScanType = %v, want ScanFull", p.ScanType)
	}
}
