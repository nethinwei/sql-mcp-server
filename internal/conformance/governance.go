package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
				{Name: "id"}, {Name: "cat"}, {Name: "val", Alias: "amount"}, {Name: "price"},
				{Name: "name", Mask: "secret"},
			},
			Relationships: []config.RelationshipConfig{
				{Name: "category", Target: "conf_cat", Cardinality: "belongs-to", JoinOn: map[string]string{"cat": "cat"}},
			},
		}, {
			Name: "conf_cat", Source: CategoryTable, Schema: schema, Kind: "table", PrimaryKey: []string{"cat"},
			Fields: []config.FieldConfig{{Name: "cat"}, {Name: "label"}, {Name: "tier"}},
		}},
		Roles: map[string]config.RoleDefinition{
			"analyst": {Grants: []config.GrantConfig{{
				Entity: "conf", Actions: []string{"read", "aggregate"},
				Fields: &config.FieldACLConfig{Read: []string{"id", "cat", "val", "name"}},
				Rows:   config.FilterConfig{"op": "eq", "field": "cat", "value": governedCategory},
			}}},
			// The scanner sees every row, and the categories with a tier.
			"scanner": {Grants: []config.GrantConfig{{Entity: "conf", Actions: []string{"read"}}, {
				Entity: "conf_cat", Actions: []string{"read"}, Rows: config.FilterConfig{"op": "is_not_null", "field": "tier"},
			}}},
			"writer": {Grants: []config.GrantConfig{{Entity: "conf", Actions: []string{"read", "update"}}}},
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
	t.Run("ordered-pages", func(t *testing.T) { orderedPages(t, app) })
	t.Run("related-row-policy", func(t *testing.T) { relatedRowPolicy(t, app) })
	t.Run("field-alias", func(t *testing.T) { fieldAlias(t, app) })
	t.Run("masked-order-refusal", func(t *testing.T) {
		refused(t, app, "analyst", tool.ReadTool{}, map[string]any{
			"entity": "conf", "orderBy": []map[string]any{{"field": "name"}},
		}, tool.CodeInvalidInput)
	})
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

// adversarialFilter: a caller's filter narrows the policy, never widens it,
// an OR or a NOT of it included.
func adversarialFilter(t *testing.T, app *bootstrap.App) {
	for _, filter := range []map[string]any{
		{"field": "cat", "op": "eq", "value": "b"},
		{"field": "cat", "op": "ne", "value": governedCategory},
		{"all": []map[string]any{{"field": "cat", "op": "eq", "value": "b"},
			{"any": []map[string]any{{"field": "val", "op": "is_null"}, {"field": "val", "op": "is_not_null"}}}}},
		{"not": map[string]any{"field": "cat", "op": "eq", "value": governedCategory}},
	} {
		res, err := run(t, app, "analyst", tool.ReadTool{}, map[string]any{
			"entity": "conf", "fields": []string{"id"}, "filter": []map[string]any{filter},
		})
		if err != nil || len(res.Content) != 0 {
			t.Fatalf("filter %v weakened the row policy: %v %v", filter, res.Content, err)
		}
	}
}

// orderedPages: an ordered read paged by nextCursor (val nullable, NULLs
// last, then the key) gives the policy's rows in the interpreter's order.
func orderedPages(t *testing.T, app *bootstrap.App) {
	var got [][]any
	input := map[string]any{"entity": "conf", "fields": []string{"id", "val"}, "limit": 2,
		"orderBy": []map[string]any{{"field": "val", "dir": "desc"}}}
	for pages := 0; ; pages++ {
		res, err := run(t, app, "analyst", tool.ReadTool{}, input)
		if err != nil || pages > 20 {
			t.Fatalf("page %d: %v", pages, err)
		}
		for _, row := range res.Content {
			got = append(got, []any{row["id"], row["val"]})
		}
		if res.NextCursor == nil {
			break
		}
		input["cursor"] = res.NextCursor
	}
	want, err := interp.Eval(Fixture(), relalg.Project{
		Input: relalg.Sort{
			Input: relalg.Select{
				Input:     relalg.Scan{Relation: relalg.RelationRef{Name: TableName}},
				Predicate: relalg.Condition{Field: "cat", Op: relalg.OpEq, Value: governedCategory},
			},
			OrderBy: []relalg.OrderTerm{{Field: "val", Dir: "desc", Nullable: true}, {Field: "id", Dir: "asc"}},
		},
		Items: []relalg.ProjectItem{{Field: "id"}, {Field: "val"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := compareRows(want.Rows, got, true); err != nil {
		t.Fatal(err)
	}
}

// relatedRowPolicy: a filter across a relationship sees the related rows
// the caller may read only (category b has no tier): joined, its rows match
// no label; by exists, no row finds it.
func relatedRowPolicy(t *testing.T, app *bootstrap.App) {
	for label, input := range map[string]map[string]any{
		"apple":  {"filter": []map[string]any{{"field": "category.label", "op": "eq", "value": "apple"}}},
		"banana": {"filter": []map[string]any{{"field": "category.label", "op": "eq", "value": "banana"}}},
		"exists banana": {"filter": []map[string]any{{"exists": "category",
			"filter": []map[string]any{{"field": "label", "op": "eq", "value": "banana"}}}}},
	} {
		input["entity"], input["fields"] = "conf", []string{"id"}
		res, err := run(t, app, "scanner", tool.ReadTool{}, input)
		if err != nil {
			t.Fatal(label, err)
		}
		want, err := interp.Eval(Fixture(), relalg.Project{Items: relalg.ProjectFields([]string{"id"}),
			Input: relalg.Select{
				Input: relalg.Join{Input: relalg.Scan{Relation: relalg.RelationRef{Name: TableName}},
					Right: relalg.RelationRef{Name: CategoryTable}, Alias: "category",
					On:             []relalg.JoinPair{{Left: "cat", Right: "cat"}},
					RightPredicate: relalg.Condition{Field: "tier", Op: relalg.OpIsNotNull}},
				Predicate: relalg.Condition{Field: "category.label", Op: relalg.OpEq,
					Value: strings.TrimPrefix(label, "exists ")},
			}})
		if err != nil {
			t.Fatal(err)
		}
		got := make([][]any, len(res.Content))
		for i, row := range res.Content {
			got[i] = []any{row["id"]}
		}
		if err := compareRows(want.Rows, got, false); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
	}
}

// fieldAlias: a field named by its alias (val as amount) is its column when
// selected, filtered and ordered by, and comes back by the alias.
func fieldAlias(t *testing.T, app *bootstrap.App) {
	res, err := run(t, app, "scanner", tool.ReadTool{}, map[string]any{
		"entity": "conf", "fields": []string{"id", "amount"},
		"filter":  []map[string]any{{"field": "amount", "op": "gt", "value": 0}},
		"orderBy": []map[string]any{{"field": "amount", "dir": "desc"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want, err := interp.Eval(Fixture(), relalg.Project{Items: relalg.ProjectFields([]string{"id", "val"}),
		Input: relalg.Sort{
			Input: relalg.Select{
				Input:     relalg.Scan{Relation: relalg.RelationRef{Name: TableName}},
				Predicate: relalg.Condition{Field: "val", Op: relalg.OpGt, Value: int64(0)},
			},
			OrderBy: []relalg.OrderTerm{{Field: "val", Dir: "desc", Nullable: true}, {Field: "id", Dir: "asc"}},
		}})
	if err != nil {
		t.Fatal(err)
	}
	got := make([][]any, len(res.Content))
	for i, row := range res.Content {
		if _, ok := row["val"]; ok {
			t.Fatalf("the field came back by its column, not its alias: %v", row)
		}
		got[i] = []any{row["id"], row["amount"]}
	}
	if err := compareRows(want.Rows, got, true); err != nil {
		t.Fatal(err)
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
