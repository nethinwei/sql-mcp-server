package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/provider"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
	"github.com/nethinwei/sql-mcp-server/core/relalg/interp"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/core/tool"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
)

// governedCategory is the rows the analyst role may read: cat = 'a'.
const governedCategory = "a"

// governanceConfig serves the fixture as entity "conf" to three roles: an
// analyst limited to category a, without price and with name masked; a
// scanner with every row; a writer. With gated, the cost gate rejects every
// full scan, even of the 12-row fixture; otherwise only its mandatory layers
// run.
func governanceConfig(driver, schema string, gated bool) *config.Config {
	cfg := &config.Config{
		Database: config.DatabaseConfig{Driver: driver, DSN: "ignored"},
		Entities: []config.EntityConfig{{
			Name: "conf", Source: TableName, Schema: schema, Kind: "table", PrimaryKey: []string{"id"},
			Fields: []config.FieldConfig{
				{Name: "id"}, {Name: "cat"}, {Name: "val"}, {Name: "price"}, {Name: "name", Mask: "secret"},
			},
		}},
		Roles: map[string]config.RoleDefinition{
			"analyst": {Grants: []config.GrantConfig{{
				Entity: "conf", Actions: []string{"read", "aggregate"},
				Fields: &config.FieldACLConfig{Read: []string{"id", "cat", "val", "name"}},
				Rows:   config.FilterConfig{"op": "eq", "field": "cat", "value": governedCategory},
			}}},
			"scanner": {Grants: []config.GrantConfig{{Entity: "conf", Actions: []string{"read"}}}},
			"writer":  {Grants: []config.GrantConfig{{Entity: "conf", Actions: []string{"read", "update"}}}},
		},
		Tools: config.DefaultToolFlags(),
		Cost:  config.CostConfig{Enabled: new(gated), RejectFullScan: gated, MaxRows: 10000},
	}
	cfg.ApplyDefaults()
	cfg.Cost.FullScanMaxRows = 0
	return cfg
}

func assemble(t *testing.T, prov provider.Provider, driver, schema string, gated bool) *bootstrap.App {
	t.Helper()
	app, err := bootstrap.AssembleWithProvider(governanceConfig(driver, schema, gated), prov)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	return app
}

// RunGovernance runs the cross-provider governance tasks on prov, whose
// fixture Setup has loaded under schema: each must give every provider the
// same answer — the same governed rows, the same refusal codes. slow is a
// statement that runs for seconds on this database, and timed a connection
// to it opened with a statement timeout well under a second.
func RunGovernance(t *testing.T, prov provider.Provider, driver, schema, slow string, timed store.DB) {
	t.Helper()
	app := assemble(t, prov, driver, schema, false)
	gated := assemble(t, prov, driver, schema, true)
	t.Run("governed-read", func(t *testing.T) { governedRead(t, app) })
	t.Run("adversarial-filter", func(t *testing.T) { adversarialFilter(t, app) })
	t.Run("governed-aggregate", func(t *testing.T) { governedAggregate(t, app) })
	t.Run("full-scan-refusal", func(t *testing.T) {
		refused(t, gated, "scanner", tool.ReadTool{}, map[string]any{
			"entity": "conf", "filter": []map[string]any{{"field": "val", "op": "gt", "value": 0}},
		}, tool.CodeCostExceeded)
	})
	t.Run("unkeyed-write-refusal", func(t *testing.T) {
		// No filter at all is an unsafe write; a filter on no key is refused
		// by the gate's WriteGuard, which cost.enabled does not turn off.
		refused(t, app, "writer", tool.UpdateTool{}, map[string]any{
			"entity": "conf", "set": map[string]any{"name": "x"},
		}, tool.CodeUnsafeWrite)
		refused(t, app, "writer", tool.UpdateTool{}, map[string]any{
			"entity": "conf", "filter": []map[string]any{{"field": "val", "op": "gt", "value": 0}},
			"set": map[string]any{"name": "x"},
		}, tool.CodeCostExceeded)
	})
	t.Run("statement-timeout", func(t *testing.T) { statementTimeout(t, timed, slow) })
	t.Run("schema-change", func(t *testing.T) { runSchemaChange(t, prov, driver, schema) })
}

func run(t *testing.T, app *bootstrap.App, role string, tl tool.Tool, input map[string]any) (tool.Result, error) {
	t.Helper()
	in, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return tool.RunTool(context.Background(), tl, in, app.ToolContext(role))
}

// governedRead: the row policy, the field ACL and the mask apply alike — the
// interpreter's rows of category a, without price, name masked.
func governedRead(t *testing.T, app *bootstrap.App) {
	res, err := run(t, app, "analyst", tool.ReadTool{}, map[string]any{
		"entity": "conf", "fields": []string{"id", "cat", "val", "name"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want, err := interp.Eval(Fixture(), relalg.Project{
		Input: relalg.Select{
			Input:     relalg.Scan{Relation: relalg.RelationRef{Name: TableName}},
			Predicate: relalg.Condition{Field: "cat", Op: relalg.OpEq, Value: governedCategory},
		},
		Items: []relalg.ProjectItem{{Field: "id"}, {Field: "cat"}, {Field: "val"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := make([][]any, len(res.Content))
	for i, row := range res.Content {
		if _, ok := row["price"]; ok {
			t.Fatalf("the field ACL leaked price: %v", row)
		}
		if row["name"] != "***" {
			t.Fatalf("name not masked: %v", row)
		}
		got[i] = []any{row["id"], row["cat"], row["val"]}
	}
	if err := compareRows(want.Rows, got, false); err != nil {
		t.Fatal(err)
	}
}

// adversarialFilter: a caller's filter narrows the policy, never widens it.
func adversarialFilter(t *testing.T, app *bootstrap.App) {
	for _, filter := range []map[string]any{
		{"field": "cat", "op": "eq", "value": "b"},
		{"field": "cat", "op": "ne", "value": governedCategory},
	} {
		res, err := run(t, app, "analyst", tool.ReadTool{}, map[string]any{
			"entity": "conf", "fields": []string{"id"}, "filter": []map[string]any{filter},
		})
		if err != nil || len(res.Content) != 0 {
			t.Fatalf("filter %v weakened the row policy: %v %v", filter, res.Content, err)
		}
	}
}

// governedAggregate: an aggregate sees the policy's rows only.
func governedAggregate(t *testing.T, app *bootstrap.App) {
	res, err := run(t, app, "analyst", tool.AggregateTool{}, map[string]any{
		"entity": "conf", "aggregates": []map[string]any{{"func": "count"}, {"func": "sum", "field": "val"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want, err := interp.Eval(Fixture(), relalg.Aggregate{
		Input: relalg.Select{
			Input:     relalg.Scan{Relation: relalg.RelationRef{Name: TableName}},
			Predicate: relalg.Condition{Field: "cat", Op: relalg.OpEq, Value: governedCategory},
		},
		Aggregates: []relalg.AggCall{{Func: relalg.AggCount}, {Func: relalg.AggSum, Field: "val"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("aggregate rows = %v", res.Content)
	}
	row := res.Content[0]
	got := [][]any{{row["count"], row["sum_val"]}}
	if err := compareRows(want.Rows, got, false); err != nil {
		t.Fatalf("%v (row %v)", err, row)
	}
}

// refused: the call is refused with code on every provider.
func refused(t *testing.T, app *bootstrap.App, role string, tl tool.Tool, input map[string]any, code string) {
	_, err := run(t, app, role, tl, input)
	d, ok := tool.DenialFor(err, "")
	if !ok || d.Code != code {
		t.Fatalf("refusal = %+v (%v), want %s", d, err, code)
	}
}

// statementTimeout: the database stops the statement at its timeout, and the
// provider reports it as store.ErrStatementTimeout.
func statementTimeout(t *testing.T, timed store.DB, slow string) {
	start := time.Now()
	_, err := timed.ExecContext(context.Background(), slow)
	if !errors.Is(err, store.ErrStatementTimeout) {
		t.Fatalf("%s = %v, want store.ErrStatementTimeout", slow, err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("stopped after %v", elapsed)
	}
}
