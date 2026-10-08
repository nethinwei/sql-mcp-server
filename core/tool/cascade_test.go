package tool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/internal/testdialect"
)

// cascadeRegistry: deleting a customer cascades to orders and sets
// audit.customer_id to NULL; updating customers.id cascades to orders.
func cascadeRegistry(t *testing.T, allow bool, auditExposed bool) *entity.Registry {
	t.Helper()
	customers := entity.Entity{
		Name: "customers", Source: "customers", MCP: entity.MCPFlags{DMLTools: true}, AllowCascade: allow,
		Attributes: []entity.Attribute{{Name: "id"}, {Name: "name"}},
		Keys:       []entity.Key{{Name: "pk", Columns: []string{"id"}, Primary: true}},
		Role:       entity.RoleAccess{entity.ActionDelete: {"w", "scoped"}, entity.ActionUpdate: {"w", "scoped"}},
		Cascades: []entity.Cascade{
			{Table: "orders", Entity: "orders", Columns: []string{"id"}, OnDelete: entity.FKCascade, OnUpdate: entity.FKCascade},
			{Table: "audit", Columns: []string{"id"}, OnDelete: entity.FKSetNull},
		},
	}
	orders := entity.Entity{
		Name: "orders", Source: "orders", MCP: entity.MCPFlags{DMLTools: true},
		Attributes: []entity.Attribute{{Name: "id"}, {Name: "tenant_id"}},
		Role:       entity.RoleAccess{entity.ActionDelete: {"w", "scoped"}, entity.ActionUpdate: {"w", "scoped"}},
		RowPolicies: entity.RowPolicies{
			"scoped": relalg.Condition{Field: "tenant_id", Op: relalg.OpEq, Value: int64(1)},
		},
	}
	all := []entity.Entity{customers, orders}
	if auditExposed {
		customers.Cascades[1].Entity = "audit"
		all[0] = customers
		all = append(all, entity.Entity{
			Name: "audit", Source: "audit", MCP: entity.MCPFlags{DMLTools: true},
			Attributes: []entity.Attribute{{Name: "id"}, {Name: "customer_id"}},
			Role:       entity.RoleAccess{entity.ActionUpdate: {"w", "scoped"}},
		})
	}
	reg, err := entity.NewRegistry(all)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestCascadingWritesNeedUnrestrictedPermissionOnEveryCascadedEntity(t *testing.T) {
	t.Parallel()
	deleteOne := `{"entity":"customers","filter":[{"field":"id","op":"eq","value":1}]}`
	renameOne := `{"entity":"customers","filter":[{"field":"id","op":"eq","value":1}],"set":{"name":"x"}}`
	rekeyOne := `{"entity":"customers","filter":[{"field":"id","op":"eq","value":1}],"set":{"id":2}}`
	for name, tc := range map[string]struct {
		role          string
		allow, audit  bool
		tool          Tool
		input         string
		wantForbidden bool
	}{
		"cascade into an unexposed table":     {"w", false, false, DeleteTool{}, deleteOne, true},
		"every cascaded entity permitted":     {"w", false, true, DeleteTool{}, deleteOne, false},
		"row-scoped permission on a child":    {"scoped", false, true, DeleteTool{}, deleteOne, true},
		"allowCascade admits it":              {"scoped", true, false, DeleteTool{}, deleteOne, false},
		"update of an unreferenced column":    {"scoped", false, false, UpdateTool{}, renameOne, false},
		"update of a referenced key cascades": {"scoped", false, true, UpdateTool{}, rekeyOne, true},
	} {
		t.Run(name, func(t *testing.T) {
			reg := cascadeRegistry(t, tc.allow, tc.audit)
			executed := false
			db := &store.FakeDB{ExecFn: func(context.Context, string, ...any) (store.Result, error) {
				executed = true
				return store.Result{RowsAffected: 1}, nil
			}}
			ctx := Context{
				Role: tc.role, DB: db, Dialect: testdialect.Postgres{}, Registry: reg,
				Authorizer: rbac.NewRoleAuthorizer(reg),
			}
			_, err := tc.tool.Run(context.Background(), json.RawMessage(tc.input), ctx)
			if tc.wantForbidden != errors.Is(err, ErrUnauthorized) || tc.wantForbidden == executed {
				t.Fatalf("error = %v, executed = %v, want forbidden = %v", err, executed, tc.wantForbidden)
			}
		})
	}
}

// TestCascadeChecksReachEveryLevelAndRewrittenField: deleting a customer
// cascades to orders and on to shipments, and sets notes.customer_id to NULL.
func TestCascadeChecksReachEveryLevelAndRewrittenField(t *testing.T) {
	t.Parallel()
	reg := cascadeChainRegistry(t)
	for role, wantForbidden := range map[string]bool{"deep": false, "shallow": true, "fields": true} {
		t.Run(role, func(t *testing.T) {
			executed := false
			db := &store.FakeDB{ExecFn: func(context.Context, string, ...any) (store.Result, error) {
				executed = true
				return store.Result{RowsAffected: 1}, nil
			}}
			ctx := Context{
				Role: role, DB: db, Dialect: testdialect.Postgres{}, Registry: reg, Authorizer: rbac.NewRoleAuthorizer(reg),
			}
			input := `{"entity":"customers","filter":[{"field":"id","op":"eq","value":1}]}`
			_, err := DeleteTool{}.Run(context.Background(), json.RawMessage(input), ctx)
			if wantForbidden != errors.Is(err, ErrUnauthorized) || wantForbidden == executed {
				t.Fatalf("error = %v, executed = %v, want forbidden = %v", err, executed, wantForbidden)
			}
		})
	}
}

// cascadeChainRegistry: "deep" may do everything a customer delete cascades
// to; "shallow" may not delete shipments; "fields" may not write
// notes.customer_id.
func cascadeChainRegistry(t *testing.T) *entity.Registry {
	t.Helper()
	roles := []string{"deep", "shallow", "fields"}
	reg, err := entity.NewRegistry([]entity.Entity{
		{
			Name: "customers", Source: "customers", MCP: entity.MCPFlags{DMLTools: true},
			Attributes: []entity.Attribute{{Name: "id"}},
			Keys:       []entity.Key{{Name: "pk", Columns: []string{"id"}, Primary: true}},
			Role:       entity.RoleAccess{entity.ActionDelete: roles},
			Cascades: []entity.Cascade{
				{Table: "orders", Entity: "orders", Columns: []string{"id"}, ForeignColumns: []string{"customer_id"},
					OnDelete: entity.FKCascade},
				{Table: "notes", Entity: "notes", Columns: []string{"id"}, ForeignColumns: []string{"customer_id"},
					OnDelete: entity.FKSetNull},
			},
		},
		{
			Name: "orders", Source: "orders", Attributes: []entity.Attribute{{Name: "id"}, {Name: "customer_id"}},
			Role: entity.RoleAccess{entity.ActionDelete: roles},
			Cascades: []entity.Cascade{{Table: "shipments", Entity: "shipments", Columns: []string{"id"},
				ForeignColumns: []string{"order_id"}, OnDelete: entity.FKCascade}},
		},
		{
			Name: "shipments", Source: "shipments", Attributes: []entity.Attribute{{Name: "id"}, {Name: "order_id"}},
			Role: entity.RoleAccess{entity.ActionDelete: {"deep", "fields"}},
		},
		{
			Name: "notes", Source: "notes", Attributes: []entity.Attribute{{Name: "body"}, {Name: "customer_id"}},
			Role:        entity.RoleAccess{entity.ActionUpdate: roles},
			FieldAccess: entity.FieldAccess{"fields": {Read: []string{"body"}, Write: []string{"body"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}
