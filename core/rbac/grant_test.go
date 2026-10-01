package rbac

import (
	"context"
	"math/rand"
	"reflect"
	"slices"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

var (
	rowsCN = relalg.Condition{Field: "region", Op: relalg.OpEq, Value: "CN"}
	rowsSG = relalg.Condition{Field: "region", Op: relalg.OpEq, Value: "SG"}
)

func grantRegistry(t *testing.T, tenant relalg.Predicate) *entity.Registry {
	t.Helper()
	reg, err := entity.NewRegistry([]entity.Entity{{
		Name: "customers",
		Attributes: []entity.Attribute{
			{Name: "id"}, {Name: "amount"}, {Name: "phone"}, {Name: "region"}, {Name: "tenant_id"},
		},
		Role:         entity.RoleAccess{entity.ActionRead: {"legacy"}},
		RowPolicies:  entity.RowPolicies{"legacy": rowsSG},
		FieldAccess:  entity.FieldAccess{"legacy": {Read: []string{"id", "region"}}},
		TenantPolicy: tenant,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func readGrant(id string, fields []string, rows relalg.Predicate) Grant {
	g := Grant{ID: id, Actions: []entity.Action{entity.ActionRead}, Rows: rows}
	if fields != nil {
		g.Fields = &entity.FieldPermissions{Read: fields}
	}
	return g
}

func alicePolicy(roles map[string][]Grant, direct []Grant, userRoles ...string) Policy {
	p := Policy{
		Roles:      map[string]map[string][]Grant{},
		Principals: map[string]Principal{"user:alice": {Roles: userRoles, Grants: map[string][]Grant{}}},
	}
	for role, grants := range roles {
		p.Roles[role] = map[string][]Grant{"customers": grants}
	}
	if direct != nil {
		p.Principals["user:alice"].Grants["customers"] = direct
	}
	return p
}

func authorizeAlice(t *testing.T, a Authorizer, fields ...string) Decision {
	t.Helper()
	dec, err := a.Authorize(context.Background(), Request{
		Role: "user:alice", Entity: "customers", Action: entity.ActionRead, Fields: fields,
		Subject: map[string]any{"tenant_id": "t1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return dec
}

func TestGrantAuthorizerRejectsCrossGrantFieldRowCombination(t *testing.T) {
	t.Parallel()
	policy := alicePolicy(map[string][]Grant{
		"a": {readGrant("a", []string{"id", "amount"}, rowsCN)},
		"b": {readGrant("b", []string{"id", "phone"}, rowsSG)},
	}, nil, "a", "b")
	a := NewGrantAuthorizer(grantRegistry(t, nil), policy)

	dec := authorizeAlice(t, a, "id", "phone")
	if !dec.Allowed || !reflect.DeepEqual(dec.RowFilter, rowsSG) || !reflect.DeepEqual(dec.Grants, []string{"b"}) {
		t.Fatalf("phone must only be visible on SG rows: %+v", dec)
	}
	dec = authorizeAlice(t, a, "id")
	if !dec.Allowed || !reflect.DeepEqual(dec.RowFilter, relalg.Or{Preds: []relalg.Predicate{rowsCN, rowsSG}}) {
		t.Fatalf("shared field must union rows: %+v", dec)
	}
	for _, fields := range [][]string{{"amount", "phone"}, nil} {
		dec = authorizeAlice(t, a, fields...)
		if dec.Allowed {
			t.Fatalf("fields %v combine grants and must be denied: %+v", fields, dec)
		}
		want := [][]string{{"id", "amount"}, {"id", "phone"}}
		if !reflect.DeepEqual(dec.FieldScopes, want) {
			t.Fatalf("FieldScopes = %v, want %v", dec.FieldScopes, want)
		}
	}
	dec = authorizeAlice(t, a, "region")
	if dec.Allowed || dec.FieldScopes != nil || dec.Reason != `field "region" is not readable by role "user:alice"` {
		t.Fatalf("field outside every grant must be a plain denial: %+v", dec)
	}
}

func TestGrantAuthorizerUnrestrictedGrantWidensRowsNotTenant(t *testing.T) {
	t.Parallel()
	tenant := relalg.Condition{Field: "tenant_id", Op: relalg.OpEq, Value: "${subject.tenant_id}"}
	resolvedTenant := relalg.Condition{Field: "tenant_id", Op: relalg.OpEq, Value: "t1"}
	policy := alicePolicy(map[string][]Grant{
		"a": {readGrant("a", nil, rowsCN)},
		"b": {readGrant("b", nil, nil)},
	}, nil, "a", "b")
	a := NewGrantAuthorizer(grantRegistry(t, tenant), policy)
	dec := authorizeAlice(t, a)
	if !dec.Allowed || !reflect.DeepEqual(dec.RowFilter, resolvedTenant) {
		t.Fatalf("unrestricted grant must still keep the tenant boundary: %+v", dec)
	}
	if len(dec.Fields) != 5 {
		t.Fatalf("default projection = %v", dec.Fields)
	}

	policy = alicePolicy(map[string][]Grant{"a": {readGrant("a", nil, rowsCN)}}, nil, "a")
	dec = authorizeAlice(t, NewGrantAuthorizer(grantRegistry(t, tenant), policy))
	want := relalg.And{Preds: []relalg.Predicate{resolvedTenant, rowsCN}}
	if !dec.Allowed || !reflect.DeepEqual(dec.RowFilter, want) {
		t.Fatalf("tenant policy must be ANDed with grant rows: %+v", dec)
	}
}

func TestGrantAuthorizerMergesDirectTopLevelAndEntityGrants(t *testing.T) {
	t.Parallel()
	policy := alicePolicy(
		map[string][]Grant{"legacy": {readGrant("role:legacy", []string{"id", "region"}, rowsCN)}},
		[]Grant{readGrant("user:alice", []string{"id"}, nil)},
		"legacy",
	)
	a := NewGrantAuthorizer(grantRegistry(t, nil), policy)
	dec := authorizeAlice(t, a, "id")
	if !dec.Allowed || dec.RowFilter != nil {
		t.Fatalf("direct unrestricted grant must widen rows for id: %+v", dec)
	}
	if !slices.Equal(dec.Grants, []string{"user:alice", "role:legacy", "entity:customers:legacy"}) {
		t.Fatalf("Grants = %v", dec.Grants)
	}
	dec = authorizeAlice(t, a, "id", "region")
	want := relalg.Or{Preds: []relalg.Predicate{rowsCN, rowsSG}}
	if !dec.Allowed || !reflect.DeepEqual(dec.RowFilter, want) {
		t.Fatalf("region must only use grants that cover it: %+v", dec)
	}
}

func TestGrantAuthorizerPrincipalDoesNotFallBackToRoleName(t *testing.T) {
	t.Parallel()
	policy := alicePolicy(nil, nil)
	a := NewGrantAuthorizer(grantRegistry(t, nil), policy)
	if dec := authorizeAlice(t, a, "id"); dec.Allowed {
		t.Fatalf("user without roles must be denied: %+v", dec)
	}
	dec, err := a.Authorize(context.Background(), Request{Role: "Legacy", Entity: "customers", Action: entity.ActionRead})
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Allowed || !reflect.DeepEqual(dec.RowFilter, rowsSG) {
		t.Fatalf("role principal must keep entity-level behavior: %+v", dec)
	}
}

func TestGrantAuthorizerChecksActionAndWriteCoverage(t *testing.T) {
	t.Parallel()
	update := Grant{
		ID: "w", Actions: []entity.Action{entity.ActionUpdate},
		Fields: &entity.FieldPermissions{Read: []string{"id"}, Write: []string{"amount"}},
	}
	policy := alicePolicy(map[string][]Grant{"a": {readGrant("r", nil, nil), update}}, nil, "a")
	a := NewGrantAuthorizer(grantRegistry(t, nil), policy)
	ctx := context.Background()
	dec, _ := a.Authorize(ctx, Request{Role: "user:alice", Entity: "customers", Action: entity.ActionAggregate})
	if dec.Allowed {
		t.Fatal("read grant must not allow aggregate")
	}
	dec, _ = a.Authorize(ctx, Request{
		Role: "user:alice", Entity: "customers", Action: entity.ActionUpdate,
		ReadFields: []string{"id"}, WriteFields: []string{"amount"},
	})
	if !dec.Allowed || !slices.Equal(dec.Grants, []string{"w"}) {
		t.Fatalf("covered update denied: %+v", dec)
	}
	dec, _ = a.Authorize(ctx, Request{
		Role: "user:alice", Entity: "customers", Action: entity.ActionUpdate, WriteFields: []string{"phone"},
	})
	if dec.Allowed || dec.Reason != `field "phone" is not writable by role "user:alice"` {
		t.Fatalf("uncovered write allowed: %+v", dec)
	}
}

// TestGrantAuthorizerCoverageInvariant checks on random grant sets that every
// allowed decision only uses grants covering all fields it touches, and that
// its row filter is exactly the OR of those grants' rows.
func TestGrantAuthorizerCoverageInvariant(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewSource(20260930))
	reg := grantRegistry(t, nil)
	for iter := 0; iter < 500; iter++ {
		grants := randomGrants(rng)
		a := NewGrantAuthorizer(reg, alicePolicy(nil, grants))
		var requested []string
		for _, f := range coverageFields {
			if rng.Intn(3) == 0 {
				requested = append(requested, f)
			}
		}
		if dec := authorizeAlice(t, a, requested...); dec.Allowed {
			assertCovered(t, iter, dec, grants)
		}
	}
}

var coverageFields = []string{"id", "amount", "phone", "region"}

func randomGrants(rng *rand.Rand) []Grant {
	rowChoices := []relalg.Predicate{nil, rowsCN, rowsSG}
	var grants []Grant
	for i := 0; i < 1+rng.Intn(3); i++ {
		var subset []string
		if rng.Intn(4) > 0 {
			subset = []string{}
			for _, f := range coverageFields {
				if rng.Intn(2) == 0 {
					subset = append(subset, f)
				}
			}
		}
		grants = append(grants, readGrant(string(rune('a'+i)), subset, rowChoices[rng.Intn(3)]))
	}
	return grants
}

func assertCovered(t *testing.T, iter int, dec Decision, grants []Grant) {
	t.Helper()
	byID := map[string]Grant{}
	for _, g := range grants {
		byID[g.ID] = g
	}
	var rows []relalg.Predicate
	unrestricted := false
	for _, id := range dec.Grants {
		g := byID[id]
		if g.Fields != nil {
			for _, f := range dec.Fields {
				if !slices.Contains(g.Fields.Read, f) {
					t.Fatalf("iter %d: grant %s does not cover field %s: %+v", iter, id, f, dec)
				}
			}
		}
		if g.Rows == nil {
			unrestricted = true
		} else {
			rows = append(rows, g.Rows)
		}
	}
	var want relalg.Predicate
	if !unrestricted {
		want = orPredicates(rows)
	}
	if !reflect.DeepEqual(dec.RowFilter, want) {
		t.Fatalf("iter %d: RowFilter = %#v, want %#v", iter, dec.RowFilter, want)
	}
}
