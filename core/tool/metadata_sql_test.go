package tool

import (
	"context"
	"errors"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/cost/costtest"
)

func TestMetadataStatements(t *testing.T) {
	t.Parallel()
	for sql, want := range map[string]bool{
		"SHOW TABLES":                        true,
		"  show index from orders;":          true,
		"-- list\nSHOW CREATE TABLE `a`":     true,
		"/* c */ # d\n DESCRIBE orders":      true,
		"desc `order_1002`.`checkout`":       true,
		"DESCRIBE orders id":                 true,
		"SHOW TABLES; DROP TABLE orders":     false,
		"SHOW TABLES LIKE 'a;b'":             false, // estimated as before: never a false yes
		"SHOWTABLES":                         false,
		"SHOW":                               false,
		"DESCRIBE ANALYZE SELECT * FROM big": false, // EXPLAIN ANALYZE runs the statement
		"DESC SELECT * FROM big":             false,
		"desc format=json select 1":          false,
		"DESCRIBE FOR CONNECTION 7":          false,
		"SELECT 1":                           false,
		"/* SHOW TABLES */ DELETE FROM t":    false,
		"-- SHOW TABLES":                     false,
		"":                                   false,
	} {
		if got := isMetadataStatement(sql); got != want {
			t.Errorf("%q: %v, want %v", sql, got, want)
		}
	}
}

// A metadata statement in query mode runs without an estimate while
// cost.sqlEscape.metadata allows it; refused, or in exec mode, it is
// estimated as any statement, and EXPLAIN refusing it refuses it.
func TestExecuteSQLRunsMetadataStatements(t *testing.T) {
	t.Parallel()
	run := func(metadata bool, mode string) error {
		tc := escapeContext(t, threeRows(), &durableRecorder{})
		tc.SQLEscapeEstimateRequired, tc.SQLEscapeMetadata = true, metadata
		route := tc.Escapes["crm"]
		route.Explainer = costtest.FakeExplainer{Err: errors.New("syntax error at SHOW")}
		tc.Escapes["crm"] = route
		_, err := RunTool(context.Background(), ExecuteSQLTool{}, sqlInput(t,
			map[string]any{"sql": "SHOW INDEX FROM orders", "mode": mode}), tc)
		return err
	}
	if err := run(true, "query"); err != nil {
		t.Fatalf("allowed: %v", err)
	}
	if err := run(false, "query"); !errors.Is(err, cost.ErrCostExceeded) {
		t.Fatalf("refused: %v", err)
	}
	if err := run(true, "exec"); !errors.Is(err, cost.ErrCostExceeded) {
		t.Fatalf("exec mode: %v", err)
	}
}
