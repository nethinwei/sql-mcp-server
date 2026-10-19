package tool

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/core/store/storetest"
	"github.com/nethinwei/sql-mcp-server/internal/testdialect"
)

func TestNumberKindOf(t *testing.T) {
	t.Parallel()
	for typ, want := range map[string]numberKind{
		"double precision": floatNumber, "real": floatNumber, "float": floatNumber, "double": floatNumber,
		"integer": integerNumber, "bigint": integerNumber, "tinyint(1)": integerNumber, "int": integerNumber,
		"numeric(20,2)": exactNumber, "decimal": exactNumber, "text": exactNumber, "": exactNumber,
		"interval": exactNumber, "point": exactNumber,
	} {
		if got := numberKindOf(typ); got != want {
			t.Errorf("numberKindOf(%q) = %v, want %v", typ, got, want)
		}
	}
}

// ledgerContext writes to ledger, whose amount is a decimal, ratio a
// double and qty an integer; a row policy keeps amount at most 100.
func ledgerContext(t *testing.T, rec *recorder) Context {
	t.Helper()
	e := entity.Entity{
		Name: "ledger", Source: "ledger",
		Attributes: []entity.Attribute{
			{Name: "id", Domain: entity.Domain{Type: "bigint"}},
			{Name: "amount", Domain: entity.Domain{Type: "numeric(30,2)"}},
			{Name: "ratio", Domain: entity.Domain{Type: "double precision"}},
			{Name: "qty", Domain: entity.Domain{Type: "integer"}},
		},
		Keys: []entity.Key{{Columns: []string{"id"}, Primary: true}},
		Role: entity.RoleAccess{entity.ActionCreate: {"w"}, entity.ActionUpdate: {"w"}, entity.ActionRead: {"w"}},
		RowPolicies: entity.RowPolicies{"w": relalg.Condition{
			Field: "amount", Op: relalg.OpLte, Value: int64(1000000000000000000),
		}},
		MCP: entity.MCPFlags{DMLTools: true},
	}
	reg, err := entity.NewRegistry([]entity.Entity{e})
	if err != nil {
		t.Fatal(err)
	}
	record := func(sql string, a []any) { rec.sqls, rec.args = append(rec.sqls, sql), append(rec.args, a) }
	return Context{
		Services: &Services{Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg)},
		Role:     "w",
		DB: &storetest.FakeDB{
			ExecFn: func(_ context.Context, sql string, a ...any) (store.Result, error) {
				record(sql, a)
				return store.Result{RowsAffected: 1}, nil
			},
			QueryFn: func(_ context.Context, sql string, a ...any) (store.Rows, error) {
				record(sql, a)
				return storetest.NewFakeRows([]string{"id"}, []any{int64(1)}), nil
			},
		},
		Dialect: testdialect.Postgres{},
	}
}

// A decimal keeps its digits, a double becomes a float64, and a fraction
// for an integer is refused before anything runs.
func TestNumbersTakeTheirColumnsKind(t *testing.T) {
	t.Parallel()
	var rec recorder
	tc := ledgerContext(t, &rec)
	if _, err := (CreateTool{}).Run(context.Background(), json.RawMessage(
		`{"entity":"ledger","values":{"id":1,"amount":12345678901234567.89,"ratio":0.5,"qty":3}}`), tc); err != nil {
		t.Fatal(err)
	}
	if args := rec.args[0]; !slices.Contains(args, any("12345678901234567.89")) ||
		!slices.Contains(args, any(0.5)) || !slices.Contains(args, any(int64(3))) {
		t.Fatalf("insert args = %#v", args)
	}
	_, err := (CreateTool{}).Run(context.Background(), json.RawMessage(
		`{"entity":"ledger","values":{"id":2,"qty":1.5}}`), tc)
	if !errors.Is(err, ErrInvalidInput) || len(rec.sqls) != 1 {
		t.Fatalf("a fraction for an integer: %v", err)
	}
	if _, err := (ReadTool{}).Run(context.Background(), json.RawMessage(
		`{"entity":"ledger","filter":[{"field":"amount","op":"in","value":[0.1,2]},`+
			`{"field":"ratio","op":"gt","value":1e-3}]}`), tc); err != nil {
		t.Fatal(err)
	}
	if args := rec.args[1]; !slices.Contains(args, any("0.1")) || !slices.Contains(args, any(int64(2))) ||
		!slices.Contains(args, any(0.001)) {
		t.Fatalf("filter args = %#v", args)
	}
	if _, err := (UpdateTool{}).Run(context.Background(), json.RawMessage(
		`{"entity":"ledger","filter":[{"field":"id","op":"eq","value":1}],"set":{"amount":0.30}}`), tc); err != nil {
		t.Fatal(err)
	}
	if args := rec.args[2]; !slices.Contains(args, any("0.30")) {
		t.Fatalf("update args = %#v", args)
	}
}

// The row scope compares a decimal as the number it holds.
func TestRowScopeComparesDecimalsExactly(t *testing.T) {
	t.Parallel()
	var rec recorder
	tc := ledgerContext(t, &rec)
	if _, err := (CreateTool{}).Run(context.Background(), json.RawMessage(
		`{"entity":"ledger","values":{"id":1,"amount":999999999999999999.99}}`), tc); err != nil {
		t.Fatalf("within the scope: %v", err)
	}
	_, err := (CreateTool{}).Run(context.Background(), json.RawMessage(
		`{"entity":"ledger","values":{"id":2,"amount":1000000000000000000.01}}`), tc)
	var violation *RowPolicyViolationError
	if !errors.As(err, &violation) {
		t.Fatalf("past the scope by a cent: %v", err)
	}
}

// Numbers no column claims (a procedure's arguments, execute_sql's) reach
// the driver as their literal text.
func TestUnclaimedNumbersAreLiteral(t *testing.T) {
	t.Parallel()
	args := []any{json.Number("1.50"), []any{json.Number("2e3")}, int64(1)}
	literalNumbers(args)
	if args[0] != "1.50" || args[1].([]any)[0] != "2e3" || args[2] != int64(1) {
		t.Fatalf("args = %#v", args)
	}
}

// A proxy's subject carries the values a configured one does: the same
// tenant shares one scope (cache, singleflight) and compares as a number.
func TestNormalizedSubjectSharesTheConfiguredScope(t *testing.T) {
	t.Parallel()
	header := map[string]any{"tenant_id": json.Number("7")}
	if err := NormalizeSubject(header); err != nil {
		t.Fatal(err)
	}
	configured := map[string]any{"tenant_id": 7}
	if header["tenant_id"] != int64(7) || scopeKey("r", header) != scopeKey("r", configured) {
		t.Fatalf("header %#v, scope %q vs %q", header, scopeKey("r", header), scopeKey("r", configured))
	}
	if err := NormalizeSubject(map[string]any{"t": map[string]any{}}); err == nil {
		t.Fatal("an object attribute was accepted")
	}
}
