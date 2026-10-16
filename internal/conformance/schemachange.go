package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/provider"
	"github.com/nethinwei/sql-mcp-server/core/tool"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
)

// ddlTable is the table the schema-change task alters under a running
// configuration.
const ddlTable = "conf_ddl"

// ddlIdleTimeout is the idle timeout of the task's transactions.
const ddlIdleTimeout = 300 * time.Millisecond

// runSchemaChange: the schema changes under a running configuration, and
// every provider answers alike. A transaction idle past its timeout lets a
// schema change waiting on its locks through; a statement naming a dropped
// column or table makes its entity unavailable, rolls its transaction back,
// and raises the drift signal.
func runSchemaChange(t *testing.T, prov provider.Provider, driver, schema string) {
	r := &ddlRun{t: t, prov: prov, table: ddlTable}
	if schema != "" {
		r.table = schema + "." + ddlTable
	}
	r.ddl("DROP TABLE IF EXISTS " + r.table)
	r.ddl("CREATE TABLE " + r.table + " (id int PRIMARY KEY, v int, w int)")
	r.ddl("INSERT INTO " + r.table + " (id, v, w) VALUES (1, 1, 1), (2, 2, 2)")
	r.app = schemaChangeApp(t, prov, driver, schema)
	r.tc = r.app.ToolContext("writer")
	r.tc.Session = "s"

	// The transaction read the table, then went idle: the schema change
	// waiting on its locks goes through once the idle timeout rolls it back.
	// (OceanBase changes the schema without waiting for the transaction.)
	tx := r.begin()
	if err := r.read(tx, "id", "w"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	r.ddl("ALTER TABLE " + r.table + " DROP COLUMN w")
	if waited := time.Since(start); waited > ddlIdleTimeout+3*time.Second {
		t.Fatalf("the schema change waited %v", waited)
	}
	time.Sleep(2 * ddlIdleTimeout)
	assertEnded(t, r.read(tx, "id"), tool.OutcomeExpired)

	// A statement naming the dropped column inside a transaction.
	tx = r.begin()
	d, _ := tool.DenialFor(r.read(tx, "id", "w"), "")
	if d.Code != tool.CodeEntityUnavailable || d.Constraints["transaction"] != tool.OutcomeRolledBack ||
		!r.app.Drift.Take() {
		t.Fatalf("a dropped column in a transaction: %+v", d)
	}
	assertEnded(t, r.read(tx, "id"), tool.OutcomeRolledBack)

	// Outside a transaction the other fields still read, until the table
	// is dropped.
	if err := r.read("", "id", "v"); err != nil {
		t.Fatal(err)
	}
	r.ddl("DROP TABLE " + r.table)
	if d, _ := tool.DenialFor(r.read("", "id", "v"), ""); d.Code != tool.CodeEntityUnavailable || !r.app.Drift.Take() {
		t.Fatalf("a dropped table: %+v", d)
	}
}

// ddlRun is the schema-change task's table and the configuration serving it.
type ddlRun struct {
	t     *testing.T
	prov  provider.Provider
	table string
	app   *bootstrap.App
	tc    tool.Context
}

func (r *ddlRun) ddl(q string) {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := r.prov.ExecContext(ctx, q); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
}

func (r *ddlRun) call(tl tool.Tool, input map[string]any) (tool.Result, error) {
	in, _ := json.Marshal(input)
	return tool.RunTool(context.Background(), tl, in, r.tc)
}

// read reads fields of row 1, in transaction tx unless it is empty.
func (r *ddlRun) read(tx string, fields ...string) error {
	in := map[string]any{"entity": "ddl", "fields": fields,
		"filter": []map[string]any{{"field": "id", "op": "eq", "value": 1}}}
	if tx != "" {
		in["transaction"] = tx
	}
	_, err := r.call(tool.ReadTool{}, in)
	return err
}

func (r *ddlRun) begin() string {
	r.t.Helper()
	res, err := r.call(tool.BeginTransactionTool{}, map[string]any{"readOnly": false})
	if err != nil {
		r.t.Fatal(err)
	}
	return res.Content[0]["transaction"].(string)
}

func assertEnded(t *testing.T, err error, outcome string) {
	t.Helper()
	var ended *tool.TransactionEndedError
	if !errors.As(err, &ended) || ended.Outcome != outcome {
		t.Fatalf("the transaction: %v, want it ended (%s)", err, outcome)
	}
}

func schemaChangeApp(t *testing.T, prov provider.Provider, driver, schema string) *bootstrap.App {
	t.Helper()
	cfg := &config.Config{
		Database: config.DatabaseConfig{Driver: driver, DSN: "ignored"},
		Entities: []config.EntityConfig{{
			Name: "ddl", Source: ddlTable, Schema: schema, Kind: "table", PrimaryKey: []string{"id"},
			Fields: []config.FieldConfig{{Name: "id"}, {Name: "v"}, {Name: "w"}},
		}},
		Roles: map[string]config.RoleDefinition{"writer": {Grants: []config.GrantConfig{
			{Entity: "ddl", Actions: []string{"read", "update"}},
		}}},
		Tools: config.DefaultToolFlags(),
		// The gate runs: its EXPLAIN meets a change before the statement.
		Cost:         config.CostConfig{Enabled: new(true), MaxRows: 10000},
		Transactions: config.TransactionConfig{IdleTimeout: ddlIdleTimeout},
	}
	cfg.ApplyDefaults()
	app, err := bootstrap.AssembleWithProvider(cfg, prov)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	return app
}
