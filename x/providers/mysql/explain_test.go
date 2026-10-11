package mysql

import (
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/cost"
)

func TestParseMySQLExplainFullScan(t *testing.T) {
	t.Parallel()
	raw := `{"query_block":{"cost_info":{"query_cost":"123.45"},"table":` +
		`{"access_type":"ALL","rows_examined_per_scan":100,"using_filesort":false}}}`
	p, err := ParseExplain([]byte(raw))
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

// A full scan's table size is the rows it examines, not what the filter lets
// through; a full scan without that estimate is not sized.
func TestParseMySQLExplainTableRows(t *testing.T) {
	t.Parallel()
	raw := `{"query_block":{"nested_loop":[` +
		`{"table":{"access_type":"ALL","rows_examined_per_scan":5000000,"rows_produced_per_join":1}},` +
		`{"table":{"access_type":"ALL","rows_examined_per_scan":12,"rows_produced_per_join":1}}]}}`
	p, _ := ParseExplain([]byte(raw))
	if !p.TableSized || p.TableRows != 5000000 {
		t.Fatalf("got %+v", p)
	}
	p, _ = ParseExplain([]byte(`{"query_block":{"table":{"access_type":"ALL","rows_produced_per_join":1}}}`))
	if p.TableSized {
		t.Fatalf("a full scan without rows_examined_per_scan is sized: %+v", p)
	}
	p, _ = ParseExplain([]byte(`{"query_block":{"table":{"access_type":"ref","rows_examined_per_scan":3}}}`))
	if p.TableSized {
		t.Fatalf("a plan without a full scan is sized: %+v", p)
	}
}

func TestParseMySQLExplainConstPoint(t *testing.T) {
	t.Parallel()
	raw := `{"query_block":{"cost_info":{"query_cost":"1.0"},"table":{"access_type":"const","rows":1}}}`
	p, _ := ParseExplain([]byte(raw))
	if p.ScanType != cost.ScanPoint {
		t.Fatalf("ScanType = %v, want ScanPoint", p.ScanType)
	}
}

func TestParseMySQLExplainInvalidDegrades(t *testing.T) {
	t.Parallel()
	p, err := ParseExplain([]byte("not json"))
	if err != nil {
		t.Fatal(err)
	}
	if p.ScanType != cost.ScanUnknown {
		t.Fatalf("ScanType = %v, want ScanUnknown", p.ScanType)
	}
}

// An aggregate's table sits under grouping_operation, a join's under
// nested_loop: the plan is as risky as its worst access.
func TestParseMySQLExplainNestedTables(t *testing.T) {
	t.Parallel()
	grouped := `{"query_block":{"cost_info":{"query_cost":"271.61"},"grouping_operation":` +
		`{"using_temporary_table":true,"table":{"access_type":"range","rows_examined_per_scan":603}}}}`
	p, _ := ParseExplain([]byte(grouped))
	if p.ScanType != cost.ScanIndex || p.EstimatedRows != 603 || !p.HasTempTable || p.TotalCost != 271.61 {
		t.Fatalf("grouped = %+v", p)
	}
	joined := `{"query_block":{"nested_loop":[` +
		`{"table":{"access_type":"eq_ref","rows_examined_per_scan":1}},` +
		`{"table":{"access_type":"ALL","rows_examined_per_scan":5000}}]}}`
	p, _ = ParseExplain([]byte(joined))
	if p.ScanType != cost.ScanFull || p.EstimatedRows != 5000 {
		t.Fatalf("joined = %+v", p)
	}
}

// A join may produce far more rows than either table is scanned for.
func TestParseMySQLExplainKeepsJoinEstimate(t *testing.T) {
	t.Parallel()
	joined := `{"query_block":{"nested_loop":[` +
		`{"table":{"access_type":"range","rows_examined_per_scan":1000,"rows_produced_per_join":1000}},` +
		`{"table":{"access_type":"ref","rows_examined_per_scan":1000,"rows_produced_per_join":1000000}}]}}`
	p, _ := ParseExplain([]byte(joined))
	if p.EstimatedRows != 1000000 || p.ScanType != cost.ScanIndex {
		t.Fatalf("joined = %+v", p)
	}
}

// A plan the optimizer settles without reading a table reads nothing.
func TestParseMySQLExplainReadsNoTable(t *testing.T) {
	t.Parallel()
	for _, message := range []string{
		"no matching row in const table", "Impossible WHERE", "Impossible WHERE noticed after reading const tables",
		"Select tables optimized away", "No matching min/max row", "Zero limit",
	} {
		raw := `{"query_block":{"select_id":1,"message":"` + message + `"}}`
		p, _ := ParseExplain([]byte(raw))
		if p.ScanType != cost.ScanPoint || p.EstimatedRows != 0 {
			t.Errorf("%q: %+v, want a point read of no rows", message, p)
		}
	}
	// Any other message, or one beside a table read, changes nothing.
	if p, _ := ParseExplain([]byte(`{"query_block":{"message":"something new"}}`)); p.ScanType != cost.ScanUnknown {
		t.Errorf("unknown message: %+v", p)
	}
	raw := `{"query_block":{"table":{"access_type":"ALL","rows_examined_per_scan":9,` +
		`"message":"Impossible WHERE"}}}`
	if p, _ := ParseExplain([]byte(raw)); p.ScanType != cost.ScanFull {
		t.Errorf("a message beside a table read: %+v", p)
	}
}

func TestParseMySQLExplainInsertValues(t *testing.T) {
	t.Parallel()
	raw := `{"query_block":{"select_id":1,"table":{"insert":true,"table_name":"stock_log","access_type":"ALL"}}}`
	p, _ := ParseExplain([]byte(raw))
	if p.ScanType != cost.ScanPoint || p.EstimatedRows != 0 {
		t.Fatalf("INSERT ... VALUES: %+v, want a point read of no rows", p)
	}
	// INSERT ... SELECT reads its source.
	raw = `{"query_block":{"select_id":1,"table":{"insert":true,"table_name":"stock_log","access_type":"ALL"},` +
		`"insert_from":{"table":{"table_name":"stock","access_type":"ALL","rows_examined_per_scan":900}}}}`
	if p, _ = ParseExplain([]byte(raw)); p.ScanType != cost.ScanFull || p.EstimatedRows != 900 {
		t.Fatalf("INSERT ... SELECT: %+v, want the source's full scan", p)
	}
}
