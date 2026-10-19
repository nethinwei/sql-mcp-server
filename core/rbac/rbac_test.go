package rbac

import (
	"context"
	"reflect"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
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

func TestAuthorizeNormalizesRole(t *testing.T) {
	t.Parallel()
	a := NewRoleAuthorizer(testRegistry(t))
	dec, err := a.Authorize(context.Background(), Request{Role: " Reader ", Entity: "users", Action: entity.ActionRead})
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Allowed || dec.RowFilter == nil {
		t.Fatalf("normalized role denied: %+v", dec)
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

func TestAuthorizeResolvesSubjectPlaceholder(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name: "docs", Source: "docs",
		Attributes: []entity.Attribute{{Name: "id"}, {Name: "tenant_id"}},
		Role:       entity.RoleAccess{entity.ActionRead: {"reader"}},
		RowPolicies: entity.RowPolicies{
			"reader": relalg.Condition{Field: "tenant_id", Op: relalg.OpEq, Value: "${subject.tenant_id}"},
		},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	a := NewRoleAuthorizer(reg)
	dec, err := a.Authorize(context.Background(), Request{
		Role: "reader", Entity: "docs", Action: entity.ActionRead,
		Subject: map[string]any{"tenant_id": int64(42)},
	})
	if err != nil {
		t.Fatal(err)
	}
	cond, ok := dec.RowFilter.(relalg.Condition)
	if !ok {
		t.Fatalf("row filter type = %T", dec.RowFilter)
	}
	if cond.Value != int64(42) {
		t.Fatalf("placeholder not resolved: %v", cond.Value)
	}
	// Missing subject attribute -> nil (fail-closed, matches no rows).
	dec2, _ := a.Authorize(context.Background(), Request{Role: "reader", Entity: "docs", Action: entity.ActionRead})
	if c := dec2.RowFilter.(relalg.Condition); c.Value != nil {
		t.Fatalf("missing subject should resolve to nil, got %v", c.Value)
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

func TestAuthorizeFieldACL(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name:       "users",
		Source:     "users",
		Attributes: []entity.Attribute{{Name: "id"}, {Name: "email"}, {Name: "secret", Excluded: true}},
		Role: entity.RoleAccess{
			entity.ActionRead:   {"editor", "blind"},
			entity.ActionUpdate: {"editor"},
		},
		FieldAccess: entity.FieldAccess{
			"editor": {Read: []string{"id"}, Write: []string{"email"}},
			"blind":  {},
		},
	}
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	a := NewRoleAuthorizer(reg)

	allowed, _ := a.Authorize(context.Background(), Request{
		Role: "editor", Entity: "users", Action: entity.ActionUpdate,
		ReadFields: []string{"id"}, WriteFields: []string{"email"},
	})
	if !allowed.Allowed {
		t.Fatalf("expected allowed field combination: %s", allowed.Reason)
	}
	denied, _ := a.Authorize(context.Background(), Request{
		Role: "editor", Entity: "users", Action: entity.ActionUpdate,
		ReadFields: []string{"email"}, WriteFields: []string{"email"},
	})
	if denied.Allowed {
		t.Fatal("write-only field must not be usable in a filter")
	}
	read, _ := a.Authorize(context.Background(), Request{
		Role: "editor", Entity: "users", Action: entity.ActionRead,
	})
	if len(read.Fields) != 1 || read.Fields[0] != "id" {
		t.Fatalf("default projection = %v, want [id]", read.Fields)
	}
	blind, _ := a.Authorize(context.Background(), Request{
		Role: "blind", Entity: "users", Action: entity.ActionRead,
	})
	if blind.Allowed {
		t.Fatal("configured role with no readable fields must fail closed")
	}
}

// System permissions are held by configured users only, directly or
// through a role; a bare role principal holds none.
func TestPolicyHasPermission(t *testing.T) {
	t.Parallel()
	policy := Policy{
		RolePermissions: map[string][]string{"dba": {"sql:execute@crm"}},
		Principals: map[string]Principal{
			"user:alice": {Roles: []string{"dba"}},
			"user:bob":   {Permissions: []string{"sql:execute@shop"}},
		},
	}
	for who, want := range map[string]map[string]bool{
		"user:alice": {"sql:execute@crm": true, "sql:execute@shop": false},
		"user:bob":   {"sql:execute@crm": false, "sql:execute@shop": true},
		"User:Alice": {"sql:execute@crm": true},
		"dba":        {"sql:execute@crm": false},
		"user:carol": {"sql:execute@crm": false},
	} {
		for permission, allowed := range want {
			if got := policy.HasPermission(who, permission); got != allowed {
				t.Errorf("HasPermission(%q, %q) = %v, want %v", who, permission, got, allowed)
			}
		}
	}
	reg, _ := entity.NewRegistry(nil)
	var checker PermissionChecker = NewGrantAuthorizer(reg, policy)
	if !checker.HasPermission("user:alice", "sql:execute@crm") {
		t.Fatal("the authorizer answers from its policy")
	}
}

// A placeholder inside an IN list resolves too: a scalar attribute is one
// item, a list attribute its items, a missing one NULL (which matches none).
func TestResolveSubjectInLists(t *testing.T) {
	t.Parallel()
	in := relalg.Condition{Field: "region", Op: relalg.OpIn,
		Value: []any{"${subject.home}", "${subject.extra}", "global", "${subject.none}"}}
	got := resolveSubject(in, map[string]any{"home": "cn", "extra": []any{"sg", "jp"}}).(relalg.Condition)
	if want := []any{"cn", "sg", "jp", "global", nil}; !reflect.DeepEqual(got.Value, want) {
		t.Fatalf("IN list = %#v, want %#v", got.Value, want)
	}
	if in.Value.([]any)[0] != "${subject.home}" {
		t.Fatal("the stored policy was changed")
	}
}
