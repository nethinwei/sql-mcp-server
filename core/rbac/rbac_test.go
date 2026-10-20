package rbac

import (
	"context"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
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

// Usable keeps only what the policy names: the roles and entity names a
// caller makes up are answered, not kept.
func TestUsableKeepsOnlyKnownPairs(t *testing.T) {
	t.Parallel()
	a := NewRoleAuthorizer(testRegistry(t))
	if !a.Usable("reader", "users") || a.Usable("stranger", "users") || a.Usable("reader", "nothing") {
		t.Fatal("answers")
	}
	kept := 0
	a.usable.Range(func(any, any) bool { kept++; return true })
	if kept != 1 {
		t.Fatalf("%d answers kept, want the known pair only", kept)
	}
	a.granted.Range(func(k, _ any) bool {
		if k.(candidatesKey).principal != "reader" {
			t.Errorf("candidates kept for %+v", k)
		}
		return true
	})
}

// projectFields keeps the requested fields a visible attribute names or
// aliases, whichever way it looks them up.
func TestProjectFieldsMatchesItsReference(t *testing.T) {
	t.Parallel()
	reference := func(visible []entity.Attribute, requested []string) []string {
		out := []string{}
		for _, f := range requested {
			for _, a := range visible {
				if a.Name == f || a.Alias != "" && a.Alias == f {
					out = append(out, f)
					break
				}
			}
		}
		return out
	}
	rng := rand.New(rand.NewPCG(3, 4))
	name := func() string { return []string{"", "a", "b", "c", "d", "e"}[rng.IntN(6)] + fmt.Sprint(rng.IntN(30)) }
	for range 2000 {
		var visible []entity.Attribute
		for range rng.IntN(40) {
			alias := ""
			if rng.IntN(3) == 0 {
				alias = name()
			}
			visible = append(visible, entity.Attribute{Name: name(), Alias: alias})
		}
		requested := []string{""}
		for range 1 + rng.IntN(30) {
			requested = append(requested, name())
		}
		if got, want := projectFields(visible, requested), reference(visible, requested); !slices.Equal(got, want) {
			t.Fatalf("projectFields(%v, %v) = %v, want %v", visible, requested, got, want)
		}
	}
}

// An entity with no visible field is never read: there is nothing to
// select, and a read of nothing would be every column, hidden ones too.
func TestReadOfNoVisibleFieldIsRefused(t *testing.T) {
	t.Parallel()
	reg, err := entity.NewRegistry([]entity.Entity{{Name: "vault",
		Attributes: []entity.Attribute{{Name: "secret", Excluded: true}},
		Role:       entity.RoleAccess{entity.ActionRead: {"analyst"}}}})
	if err != nil {
		t.Fatal(err)
	}
	dec, err := NewRoleAuthorizer(reg).Authorize(context.Background(),
		Request{Role: "analyst", Entity: "vault", Action: entity.ActionRead})
	if err != nil || dec.Allowed {
		t.Fatalf("decision = %+v, %v", dec, err)
	}
}

// An aggregate that reads no field counts the rows of every grant: their
// fields, which it does not read, narrow nothing.
func TestAggregateOfNoFieldKeepsEveryGrantsRows(t *testing.T) {
	t.Parallel()
	reg, err := entity.NewRegistry([]entity.Entity{{Name: "orders",
		Attributes: []entity.Attribute{{Name: "id"}, {Name: "amount"}, {Name: "region"}}}})
	if err != nil {
		t.Fatal(err)
	}
	rows := func(v string) relalg.Predicate { return relalg.Condition{Field: "region", Op: relalg.OpEq, Value: v} }
	grant := func(id, region string, fields ...string) Grant {
		return Grant{ID: id, Actions: []entity.Action{entity.ActionAggregate},
			Fields: &entity.FieldPermissions{Read: fields}, Rows: rows(region)}
	}
	a := NewGrantAuthorizer(reg, Policy{Roles: map[string]map[string][]Grant{"analyst": {"orders": {
		grant("a1", "eu", "id", "amount"), grant("a2", "us", "id", "region"),
	}}}})
	dec, err := a.Authorize(context.Background(), Request{Role: "analyst", Entity: "orders",
		Action: entity.ActionAggregate})
	if err != nil || !dec.Allowed || len(dec.Grants) != 2 {
		t.Fatalf("decision = %+v, %v", dec, err)
	}
	if _, or := dec.RowFilter.(relalg.Or); !or {
		t.Fatalf("rows = %#v, want both grants' rows", dec.RowFilter)
	}
}
