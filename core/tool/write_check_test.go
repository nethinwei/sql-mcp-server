package tool

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/core/store/storetest"
	"github.com/nethinwei/sql-mcp-server/internal/testdialect"
)

// recorder keeps the statements a fake database ran.
type recorder struct {
	sqls []string
	args [][]any
}

// tenantContext is a writer of tenant 7 on orders, whose tenant_id column
// (hidden unless visible) the tenant policy pins; a row policy keeps status
// off archived.
func tenantContext(t *testing.T, rec *recorder, visible bool) Context {
	t.Helper()
	e := entity.Entity{
		Name:   "orders",
		Source: "orders",
		Attributes: []entity.Attribute{
			{Name: "id"}, {Name: "tenant_id", Excluded: !visible}, {Name: "status"}, {Name: "note"},
		},
		Keys: []entity.Key{{Columns: []string{"id"}, Primary: true}},
		Role: entity.RoleAccess{entity.ActionCreate: {"w"}, entity.ActionUpdate: {"w"}},
		TenantPolicy: relalg.Condition{
			Field: "tenant_id", Op: relalg.OpEq, Value: "${subject.tenant_id}",
		},
		RowPolicies: entity.RowPolicies{"w": relalg.Condition{
			Field: "status", Op: relalg.OpNe, Value: "archived",
		}},
		MCP: entity.MCPFlags{DMLTools: true},
	}
	reg, err := entity.NewRegistry([]entity.Entity{e})
	if err != nil {
		t.Fatal(err)
	}
	db := &storetest.FakeDB{
		ExecFn: func(_ context.Context, sql string, a ...any) (store.Result, error) {
			rec.sqls, rec.args = append(rec.sqls, sql), append(rec.args, a)
			return store.Result{RowsAffected: 1}, nil
		},
		QueryFn: func(_ context.Context, sql string, a ...any) (store.Rows, error) {
			rec.sqls, rec.args = append(rec.sqls, sql), append(rec.args, a)
			return storetest.NewFakeRows([]string{"id"}, []any{int64(1)}), nil
		},
	}
	return Context{
		Services: &Services{Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg)},
		Role:     "w",
		Subject:  map[string]any{"tenant_id": int64(7)},
		DB:       db,
		Dialect:  testdialect.Postgres{},
	}
}

func TestCreatePinsTheHiddenTenantColumn(t *testing.T) {
	t.Parallel()
	var rec recorder
	tc := tenantContext(t, &rec, false)
	if _, err := (CreateTool{}).Run(context.Background(), json.RawMessage(
		`{"entity":"orders","values":{"id":1,"status":"new"}}`), tc); err != nil {
		t.Fatal(err)
	}
	if len(rec.sqls) != 1 || !strings.Contains(rec.sqls[0], `"tenant_id"`) ||
		!slices.Contains(rec.args[0], any(int64(7))) {
		t.Fatalf("insert = %q %v, want the tenant column set to 7", rec.sqls, rec.args)
	}
}

func TestCreateCannotCrossTenant(t *testing.T) {
	t.Parallel()
	for name, values := range map[string]string{
		"another tenant":   `{"id":1,"status":"new","tenant_id":8}`,
		"outside the rows": `{"id":1,"status":"archived"}`,
		"NULL row column":  `{"id":1,"status":null}`,
	} {
		var rec recorder
		_, err := (CreateTool{}).Run(context.Background(),
			json.RawMessage(`{"entity":"orders","values":`+values+`}`), tenantContext(t, &rec, true))
		var violation *RowPolicyViolationError
		if !errors.As(err, &violation) || len(rec.sqls) != 0 {
			t.Errorf("%s: err = %v, ran %q; want a row policy violation", name, err, rec.sqls)
		}
		if d, _ := DenialFor(err, "d"); d.Code != CodeRowPolicyViolation {
			t.Errorf("%s: denial %+v", name, d)
		}
	}
}

func TestCreateViolationNamesOnlyVisibleFields(t *testing.T) {
	t.Parallel()
	var rec recorder
	_, err := (CreateTool{}).Run(context.Background(), json.RawMessage(
		`{"entity":"orders","values":{"id":1,"status":"archived"}}`), tenantContext(t, &rec, false))
	var violation *RowPolicyViolationError
	if !errors.As(err, &violation) || !reflect.DeepEqual(violation.Fields, []string{"status"}) {
		t.Fatalf("err = %v, want a violation naming status only", err)
	}
}

func TestUpdateCannotMoveRowAcrossTenant(t *testing.T) {
	t.Parallel()
	for name, set := range map[string]string{
		"another tenant": `{"tenant_id":8}`,
		"archived":       `{"status":"archived"}`,
	} {
		var rec recorder
		_, err := (UpdateTool{}).Run(context.Background(), json.RawMessage(
			`{"entity":"orders","filter":[{"field":"id","op":"eq","value":1}],"set":`+set+`}`),
			tenantContext(t, &rec, true))
		var violation *RowPolicyViolationError
		if !errors.As(err, &violation) || len(rec.sqls) != 0 {
			t.Errorf("%s: err = %v, ran %q; want a row policy violation", name, err, rec.sqls)
		}
	}
}

func TestUpdateWithinScopeKeepsThePolicy(t *testing.T) {
	t.Parallel()
	var rec recorder
	if _, err := (UpdateTool{}).Run(context.Background(), json.RawMessage(
		`{"entity":"orders","filter":[{"field":"id","op":"eq","value":1}],"set":{"tenant_id":7,"note":"x"}}`),
		tenantContext(t, &rec, true)); err != nil {
		t.Fatal(err)
	}
	if len(rec.sqls) != 1 || !strings.Contains(rec.sqls[0], `"status" <> `) ||
		!strings.Contains(rec.sqls[0], `"tenant_id" = `) {
		t.Fatalf("update = %q, want the row scope in its WHERE", rec.sqls)
	}
}

func TestUpdateResidualPolicyConjoined(t *testing.T) {
	t.Parallel()
	scope := relalg.Or{Preds: []relalg.Predicate{
		relalg.Condition{Field: "status", Op: relalg.OpEq, Value: "open"},
		relalg.Condition{Field: "owner", Op: relalg.OpEq, Value: "ann"},
	}}
	e := entity.Entity{Attributes: []entity.Attribute{{Name: "status"}, {Name: "owner"}}}
	check, err := updateCheck(e, scope, map[string]any{"status": "closed"})
	want := relalg.Condition{Field: "owner", Op: relalg.OpEq, Value: "ann"}
	if err != nil || !reflect.DeepEqual(check, want) {
		t.Fatalf("check = %#v, %v; want the rows owned by ann only", check, err)
	}
}

func TestUpdateRejectsKeyColumnsByDefault(t *testing.T) {
	t.Parallel()
	var rec recorder
	tc := tenantContext(t, &rec, true)
	_, err := (UpdateTool{}).Run(context.Background(), json.RawMessage(
		`{"entity":"orders","filter":[{"field":"id","op":"eq","value":1}],"set":{"id":2}}`), tc)
	if !errors.Is(err, ErrInvalidInput) || len(rec.sqls) != 0 {
		t.Fatalf("err = %v, ran %q; want the key update refused", err, rec.sqls)
	}
	e := entity.Entity{
		Attributes:     []entity.Attribute{{Name: "id"}},
		Keys:           []entity.Key{{Columns: []string{"id"}, Primary: true}},
		AllowKeyUpdate: true,
	}
	if err := refuseKeyUpdate(e, []string{"id"}); err != nil {
		t.Fatalf("allowKeyUpdate: %v", err)
	}
}

func TestUpdateRequiresAnAssignment(t *testing.T) {
	t.Parallel()
	var rec recorder
	_, err := (UpdateTool{}).Run(context.Background(), json.RawMessage(
		`{"entity":"orders","filter":[{"field":"id","op":"eq","value":1}],"set":{}}`), tenantContext(t, &rec, true))
	if !errors.Is(err, ErrInvalidInput) || len(rec.sqls) != 0 {
		t.Fatalf("err = %v, ran %q", err, rec.sqls)
	}
}
