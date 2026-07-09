package rbac

import (
	"context"
	"testing"

	"github.com/nethinwei/sql-mcp-server/entity"
	"github.com/nethinwei/sql-mcp-server/relalg"
)

func testRegistry(t *testing.T) *entity.Registry {
	t.Helper()
	e := entity.Entity{
		Name:   "users",
		Source: "t_user",
		Attributes: []entity.Attribute{
			{Name: "id"},
			{Name: "email"},
			{Name: "phone", Excluded: true},
		},
		Role: entity.RoleAccess{
			entity.ActionRead:   {"reader", "admin"},
			entity.ActionDelete: {"admin"},
		},
		RowPolicies: entity.RowPolicies{
			"reader": relalg.Condition{Field: "tenant_id", Op: relalg.OpEq, Value: int64(7)},
		},
	}
	reg, err := entity.NewRegistry([]entity.Entity{e})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestAuthorizeAllowed(t *testing.T) {
	t.Parallel()
	a := NewRoleAuthorizer(testRegistry(t))
	dec, err := a.Authorize(context.Background(), Request{Role: "reader", Entity: "users", Action: entity.ActionRead})
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Allowed {
		t.Fatalf("denied: %s", dec.Reason)
	}
	if dec.RowFilter == nil {
		t.Error("expected row filter injected for reader")
	}
}

func TestAuthorizeRoleDenied(t *testing.T) {
	t.Parallel()
	a := NewRoleAuthorizer(testRegistry(t))
	dec, _ := a.Authorize(context.Background(), Request{Role: "reader", Entity: "users", Action: entity.ActionDelete})
	if dec.Allowed {
		t.Fatal("reader should not delete")
	}
}

func TestAuthorizeEntityNotFound(t *testing.T) {
	t.Parallel()
	a := NewRoleAuthorizer(testRegistry(t))
	dec, err := a.Authorize(context.Background(), Request{Role: "reader", Entity: "ghost", Action: entity.ActionRead})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Allowed {
		t.Fatal("expected denial for unknown entity")
	}
}

func TestAuthorizeFieldProjection(t *testing.T) {
	t.Parallel()
	a := NewRoleAuthorizer(testRegistry(t))
	// request id and phone; phone is excluded -> must not appear
	dec, _ := a.Authorize(context.Background(), Request{
		Role: "admin", Entity: "users", Action: entity.ActionRead, Fields: []string{"id", "phone", "missing"},
	})
	if !dec.Allowed {
		t.Fatal("admin should read")
	}
	if len(dec.Fields) != 1 || dec.Fields[0] != "id" {
		t.Fatalf("projected fields = %v, want [id]", dec.Fields)
	}
}

func TestAuthorizeAllVisibleWhenNoFields(t *testing.T) {
	t.Parallel()
	a := NewRoleAuthorizer(testRegistry(t))
	dec, _ := a.Authorize(context.Background(), Request{Role: "admin", Entity: "users", Action: entity.ActionRead})
	// phone excluded -> only id, email
	if len(dec.Fields) != 2 {
		t.Fatalf("fields = %v, want 2 (id, email)", dec.Fields)
	}
}
