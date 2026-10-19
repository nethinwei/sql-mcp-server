package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/audit"
	"github.com/nethinwei/sql-mcp-server/core/budget"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/cost/costtest"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/core/store/storetest"
	"github.com/nethinwei/sql-mcp-server/internal/testdialect"
)

// durableRecorder is an audit.Durable fake: it records synchronously, and
// fails the phases it is told to.
type durableRecorder struct {
	recorderAuditor
	sync     []audit.Event
	failures map[string]error // by phase
}

func (d *durableRecorder) RecordSync(_ context.Context, e audit.Event) error {
	if err := d.failures[e.Phase]; err != nil {
		return err
	}
	d.sync = append(d.sync, e)
	return nil
}

// statedError is a driver error reporting a SQLSTATE, as pgconn's does.
type statedError struct{ msg, state string }

func (e statedError) Error() string    { return e.msg }
func (e statedError) SQLState() string { return e.state }

// escapeContext is alice's call on a configuration admitting the escape
// hatch on datasource crm, with the policy granting her sql:execute@crm
// through role dba; db answers its statements.
func escapeContext(t *testing.T, db store.DB, auditor audit.Auditor) Context {
	t.Helper()
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	policy := rbac.Policy{
		RolePermissions: map[string][]string{"dba": {"sql:execute@crm"}},
		Principals: map[string]rbac.Principal{
			"user:alice": {Roles: []string{"dba"}},
			"user:bob":   {Roles: []string{"reader"}},
		},
	}
	return Context{
		Services: &Services{
			Registry: reg, Authorizer: rbac.NewGrantAuthorizer(reg, policy), Auditor: auditor,
			Limits: Limits{MaxRows: 2, SQLEscapeEstimateRequired: true, Timeout: time.Second},
			Escapes: map[string]EscapeRoute{"crm": {
				Connection: "ops", DB: db, Dialect: testdialect.Postgres{},
				Explainer: costtest.FakeExplainer{Plan: cost.Plan{ScanType: cost.ScanIndex, EstimatedRows: 1, StatsFresh: true}},
				Threshold: cost.Threshold{HardScore: 40, RejectFullScan: true, Datasource: "crm", DialectName: "postgres"},
			}},
		},
		Role: "user:alice", User: "alice", UserRoles: []string{"dba"}, Session: "s1",
	}
}

func threeRows() *storetest.FakeDB {
	return &storetest.FakeDB{
		QueryFn: func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
			return storetest.NewFakeRows([]string{"id", "email"},
				[]any{int64(1), "a@x.com"}, []any{int64(2), "b@x.com"}, []any{int64(3), "c@x.com"}), nil
		},
		ExecFn: func(_ context.Context, _ string, _ ...any) (store.Result, error) {
			return store.Result{RowsAffected: 7}, nil
		},
	}
}

func sqlInput(t *testing.T, fields map[string]any) json.RawMessage {
	t.Helper()
	in := map[string]any{"datasource": "crm", "sql": "select id, email from users"}
	for k, v := range fields {
		in[k] = v
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Every condition of the hatch is checked on the call: the datasource must
// admit it (none does while tools.executeSql is off, see bootstrap) and the
// user must hold its permission; a bare role never does. A refused call
// reaches neither the database nor the audit log's "begin".
func TestExecuteSQLMatrix(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		who, datasource string
		admitted        bool
		allowed         bool
	}{
		"admitted and permitted":    {"user:alice", "crm", true, true},
		"not admitted":              {"user:alice", "crm", false, false},
		"not permitted":             {"user:bob", "crm", true, false},
		"a role, not a user":        {"dba", "crm", true, false},
		"another datasource":        {"user:alice", "other", true, false},
		"neither admitted nor held": {"user:bob", "crm", false, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			db, auditor := threeRows(), &durableRecorder{}
			ctx := escapeContext(t, db, auditor)
			ctx.Role = tc.who
			if !tc.admitted {
				ctx.Escapes = nil
			}
			input := sqlInput(t, map[string]any{"datasource": tc.datasource})
			res, err := RunTool(context.Background(), ExecuteSQLTool{}, input, ctx)
			if tc.allowed {
				if err != nil || len(res.Content) != 1 {
					t.Fatalf("allowed call: %v %+v", err, res)
				}
				return
			}
			if !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("err = %v, want unauthorized", err)
			}
			if len(db.Queries) != 0 || len(db.Execs) != 0 {
				t.Fatal("a refused call reached the database")
			}
			for _, ev := range auditor.sync {
				if ev.Phase == "begin" {
					t.Fatal("a refused call recorded a begin event")
				}
			}
			if d, _ := DenialFor(err, ""); d.Code != CodeUnauthorized || d.Reason != ErrUnauthorized.Error() {
				t.Fatalf("the client learns which condition failed: %+v", d)
			}
		})
	}
}

// The hatch's rows are returned as they are, cut at the row cap (not
// refused), and its audit holds the statement before and after it ran.
func TestExecuteSQLQueryRowsAndAudit(t *testing.T) {
	t.Parallel()
	db, auditor := threeRows(), &durableRecorder{}
	tc := escapeContext(t, db, auditor)
	tc.Masker = recordingMasker{}
	res, err := RunTool(context.Background(), ExecuteSQLTool{}, sqlInput(t, map[string]any{"args": []any{1}}), tc)
	if err != nil {
		t.Fatal(err)
	}
	got := res.Content[0]
	rows, _ := got["rows"].([][]any)
	if cols, _ := got["columns"].([]string); len(cols) != 2 || len(rows) != 2 || got["truncated"] != true {
		t.Fatalf("result = %+v", got)
	}
	if rows[0][1] != "a@x.com" {
		t.Fatalf("the hatch masks nothing: %v", rows[0])
	}
	if len(db.Queries) != 1 || db.Queries[0].Query != "select id, email from users" || len(db.Queries[0].Args) != 1 {
		t.Fatalf("statement sent as it is: %+v", db.Queries)
	}
	if len(auditor.sync) != 2 || len(auditor.events) != 0 {
		t.Fatalf("two synchronous events, none asynchronous: %d/%d", len(auditor.sync), len(auditor.events))
	}
	begin, end := auditor.sync[0], auditor.sync[1]
	if begin.Phase != "begin" || begin.SQL != "select id, email from users" || begin.Datasource != "crm" ||
		begin.Connection != "ops" || begin.Action != ActionSQL || begin.User != "alice" ||
		len(begin.Grants) != 1 || begin.Grants[0] != "permission:sql:execute@crm" || begin.Cost == nil {
		t.Fatalf("begin = %+v", begin)
	}
	if end.Phase != "end" || end.SQL != begin.SQL || !end.Allowed || end.ReturnedRows != 2 ||
		end.DecisionID != begin.DecisionID {
		t.Fatalf("end = %+v", end)
	}
}

type recordingMasker struct{}

func (recordingMasker) Mask(rule string, _ any) any { return "MASKED:" + rule }

// exec reports the rows affected, and whatever the mode every cached read
// of the datasource is dropped: the hatch does not know what it changed.
func TestExecuteSQLExecAndInvalidation(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"exec", "query"} {
		db, auditor := threeRows(), &durableRecorder{}
		tc := escapeContext(t, db, auditor)
		c := &recordingCache{}
		tc.Cache = c
		tc.Writes = NewWriteTracker()
		tc.Sources = followingWrites("crm")
		res, err := RunTool(context.Background(), ExecuteSQLTool{}, sqlInput(t, map[string]any{"mode": mode}), tc)
		if err != nil {
			t.Fatal(err)
		}
		if mode == "exec" {
			if res.Content[0]["rowsAffected"] != int64(7) || len(db.Execs) != 1 || auditor.sync[1].RowsAffected != 7 {
				t.Fatalf("exec result = %+v, events %+v", res.Content, auditor.sync)
			}
		}
		if c.invalidations != 1 || !tc.Writes.Recent("s1", "crm") {
			t.Fatalf("%s: invalidations %d, write recorded %v", mode, c.invalidations,
				tc.Writes.Recent("s1", "crm"))
		}
	}
}

// The hard limits hold without any estimate: the row cap cuts, the byte cap
// refuses, and the timeout bounds the statement.
func TestExecuteSQLLimitsSurviveCostDisabled(t *testing.T) {
	t.Parallel()
	var deadline bool
	db := threeRows()
	db.QueryFn = func(ctx context.Context, _ string, _ ...any) (store.Rows, error) {
		_, deadline = ctx.Deadline()
		return storetest.NewFakeRows([]string{"id"}, []any{int64(1)}, []any{int64(2)}, []any{int64(3)}), nil
	}
	tc := escapeContext(t, db, &durableRecorder{})
	tc.SQLEscapeEstimateRequired = false
	route := tc.Escapes["crm"]
	route.Explainer = nil
	tc.Escapes["crm"] = route
	res, err := RunTool(context.Background(), ExecuteSQLTool{}, sqlInput(t, nil), tc)
	if err != nil || res.Content[0]["truncated"] != true || !deadline {
		t.Fatalf("row cap and timeout must hold: %v %+v deadline=%v", err, res.Content, deadline)
	}
	tc.MaxReturnedBytes = 8
	_, err = RunTool(context.Background(), ExecuteSQLTool{}, sqlInput(t, nil), tc)
	if !errors.Is(err, budget.ErrExceeded) {
		t.Fatalf("byte cap: %v", err)
	}
	tc.MaxReturnedBytes = 0
	tc.BudgetLimits = budget.Limits{MaxReturnedRows: 1}
	if res, err := RunTool(context.Background(), ExecuteSQLTool{}, sqlInput(t, nil), tc); err != nil ||
		res.Content[0]["rowCount"] != 1 {
		t.Fatalf("the budget's row cap cuts too: %v %+v", err, res.Content)
	}
}

// The audit is fail closed: no durable sink, or a sink failing before the
// statement, refuses the call before the database is reached; a sink
// failing after it withholds the result.
func TestExecuteSQLRefusesWhenAuditFails(t *testing.T) {
	t.Parallel()
	for name, auditor := range map[string]audit.Auditor{
		"no auditor":  nil,
		"best-effort": &recorderAuditor{},
		"begin fails": &durableRecorder{failures: map[string]error{"begin": errors.New("disk full")}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			db := threeRows()
			tc := escapeContext(t, db, auditor)
			_, err := RunTool(context.Background(), ExecuteSQLTool{}, sqlInput(t, nil), tc)
			if !errors.Is(err, ErrAuditUnavailable) || len(db.Queries) != 0 {
				t.Fatalf("err = %v, queries %d", err, len(db.Queries))
			}
			if d, _ := DenialFor(err, ""); d.Code != CodeAuditUnavailable || d.Retryable {
				t.Fatalf("denial = %+v", d)
			}
		})
	}
	t.Run("end fails", func(t *testing.T) {
		t.Parallel()
		db := threeRows()
		auditor := &durableRecorder{failures: map[string]error{"end": errors.New("disk full")}}
		res, err := RunTool(context.Background(), ExecuteSQLTool{}, sqlInput(t, nil), escapeContext(t, db, auditor))
		if !errors.Is(err, ErrAuditUnavailable) || len(res.Content) != 0 || len(db.Queries) != 1 {
			t.Fatalf("the result is withheld: err %v, content %v", err, res.Content)
		}
		if len(auditor.sync) != 1 || auditor.sync[0].Phase != "begin" {
			t.Fatalf("the begin event was written: %+v", auditor.sync)
		}
	})
}

// An estimate is required by default: EXPLAIN refusing the statement, or a
// plan over the thresholds, refuses it as cost exceeded; skipping the
// estimate runs it.
func TestExecuteSQLEstimate(t *testing.T) {
	t.Parallel()
	run := func(explainer cost.Explainer, required bool) (Result, error) {
		tc := escapeContext(t, threeRows(), &durableRecorder{})
		tc.SQLEscapeEstimateRequired = required
		route := tc.Escapes["crm"]
		route.Explainer = explainer
		tc.Escapes["crm"] = route
		input := sqlInput(t, map[string]any{"sql": "SET x = 1", "mode": "exec"})
		return RunTool(context.Background(), ExecuteSQLTool{}, input, tc)
	}
	_, err := run(costtest.FakeExplainer{Err: errors.New("syntax error at SET")}, true)
	var unestimable *UnestimableError
	if !errors.As(err, &unestimable) || !errors.Is(err, cost.ErrCostExceeded) {
		t.Fatalf("err = %v", err)
	}
	if d, _ := DenialFor(err, ""); d.Code != CodeCostExceeded || !strings.Contains(d.Reason, "syntax error at SET") {
		t.Fatalf("denial = %+v", d)
	}
	if _, err := run(costtest.FakeExplainer{Err: errors.New("syntax error")}, false); err != nil {
		t.Fatalf("skipped estimate: %v", err)
	}
	if _, err := run(nil, true); !errors.As(err, &unestimable) {
		t.Fatalf("a required estimate without EXPLAIN ran: %v", err)
	}
	full := costtest.FakeExplainer{Plan: cost.Plan{ScanType: cost.ScanFull, EstimatedRows: 1e6, StatsFresh: true}}
	if _, err := run(full, true); !errors.Is(err, cost.ErrCostExceeded) {
		t.Fatalf("full scan: %v", err)
	}
}

// The hatch reports database errors with the driver's message and SQLSTATE,
// the one exception to the contract: the governed tools keep reporting the
// bare kind for the same error.
func TestExecuteSQLEchoesDatabaseErrors(t *testing.T) {
	t.Parallel()
	cause := statedError{msg: `relation "nope" does not exist`, state: "42P01"}
	db := &storetest.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) { return nil, cause }}
	_, err := RunTool(context.Background(), ExecuteSQLTool{}, sqlInput(t, nil), escapeContext(t, db, &durableRecorder{}))
	d, ok := DenialFor(err, "")
	if !ok || d.Code != CodeDatabaseError || !strings.Contains(d.Reason, cause.msg) ||
		d.Constraints["sqlstate"] != "42P01" {
		t.Fatalf("denial = %+v (%v)", d, err)
	}
	if !errors.Is(err, ErrDatabase) {
		t.Fatal("a raw database error is still a database error")
	}
	reg, _ := entity.NewRegistry([]entity.Entity{testUsersEntity()})
	tc := Context{Services: &Services{Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg)}, Role: "reader", DB: db,
		Dialect: testdialect.Postgres{}}
	input, _ := json.Marshal(readInput{Entity: "users", Fields: []string{"id"}})
	_, err = RunTool(context.Background(), ReadTool{}, input, tc)
	if d, _ := DenialFor(err, ""); d.Code != CodeDatabaseError || strings.Contains(d.Reason, "nope") {
		t.Fatalf("the governed plane must not echo: %+v", d)
	}
	denied := &storetest.FakeDB{ExecFn: func(context.Context, string, ...any) (store.Result, error) {
		return store.Result{}, store.ErrPermissionDenied
	}}
	_, err = RunTool(context.Background(), ExecuteSQLTool{}, sqlInput(t, map[string]any{"mode": "exec"}),
		escapeContext(t, denied, &durableRecorder{}))
	if d, _ := DenialFor(err, ""); d.Code != CodeDatasourceForbidden {
		t.Fatalf("the account's limit: %+v", d)
	}
}

// The input is strict: a transaction token, an unknown field, a missing
// datasource or an unknown mode are invalid input.
func TestExecuteSQLRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	for name, fields := range map[string]map[string]any{
		"transaction":   {"transaction": "tx-1"},
		"unknown field": {"entity": "users"},
		"no datasource": {"datasource": ""},
		"bad mode":      {"mode": "both"},
		"empty sql":     {"sql": "  "},
	} {
		db := threeRows()
		tc := escapeContext(t, db, &durableRecorder{})
		_, err := RunTool(context.Background(), ExecuteSQLTool{}, sqlInput(t, fields), tc)
		if !errors.Is(err, ErrInvalidInput) || len(db.Queries) != 0 {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
}

// Only the escape hatch plans raw statements: nothing in the governed plane
// reaches authorizeSQL, estimateSQL, newRawStatement or queryRaw.
func TestOnlyExecuteSQLUsesRawStatements(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") || name == "pipeline.go" || name == "tools_execute_sql.go" {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range []string{"authorizeSQL(", "estimateSQL(", "newRawStatement(", "queryRaw("} {
			if strings.Contains(string(src), call) {
				t.Errorf("%s uses %s; only execute_sql may", name, call)
			}
		}
	}
}
