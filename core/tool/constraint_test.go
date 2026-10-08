package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/internal/testdialect"
)

func TestWriteConstraintViolationIsAnActionableDenial(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name: "users", Source: "users", MCP: entity.MCPFlags{DMLTools: true},
		Attributes: []entity.Attribute{{Name: "id"}, {Name: "email"}, {Name: "secret", Excluded: true}},
		Keys: []entity.Key{
			{Name: "pk", Columns: []string{"id"}, Primary: true},
			{Name: "users_email_key", Columns: []string{"email", "secret"}},
		},
		Role: entity.RoleAccess{entity.ActionCreate: {"w"}},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	for name, driverErr := range map[string]*store.ConstraintError{
		"columns reported":   {Kind: store.ConstraintUnique, Columns: []string{"email", "secret"}, Err: errors.New("raw")},
		"only the key named": {Kind: store.ConstraintUnique, Constraint: "users_email_key", Err: errors.New("raw")},
	} {
		t.Run(name, func(t *testing.T) {
			db := &store.FakeDB{ExecFn: func(context.Context, string, ...any) (store.Result, error) {
				return store.Result{}, driverErr
			}}
			tc := Context{
				Role: "w", DB: db, Dialect: testdialect.MySQL{}, Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg),
			}
			_, err := CreateTool{}.Run(context.Background(), json.RawMessage(`{"entity":"users","values":{"email":"a"}}`), tc)
			denial, ok := DenialFor(err, "d")
			if !ok || denial.Code != CodeConstraintViolation || !denial.Retryable {
				t.Fatalf("denial = %+v (%v)", denial, err)
			}
			fields, _ := json.Marshal(denial.Constraints)
			if string(fields) != `{"fields":["email"],"kind":"unique"}` {
				t.Fatalf("constraints = %s, want the kind and the visible fields only", fields)
			}
			if strings.Contains(denial.Reason, "users_email_key") || strings.Contains(denial.Reason, "raw") {
				t.Fatalf("reason leaks the constraint or driver message: %q", denial.Reason)
			}
		})
	}
}

func TestRefusedPrivilegeIsADatasourceForbiddenDenial(t *testing.T) {
	t.Parallel()
	raw := fmt.Errorf("%w: %w", store.ErrPermissionDenied, errors.New("permission denied for table secret_salaries"))
	err := WrapDBError(raw)
	denial, ok := DenialFor(err, "d")
	if !ok || denial.Code != CodeDatasourceForbidden || denial.Retryable || strings.Contains(denial.Reason, "salaries") {
		t.Fatalf("denial = %+v", denial)
	}
	if !errors.Is(err, raw) {
		t.Fatal("the driver error stays in the chain for the audit log")
	}
}
