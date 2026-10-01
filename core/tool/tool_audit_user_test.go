package tool

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/internal/testdialect"
)

func TestRunToolAuditsUserRolesAndCoveringGrants(t *testing.T) {
	t.Parallel()
	e := testUsersEntity()
	e.Role = nil
	reg, _ := entity.NewRegistry([]entity.Entity{e})
	policy := rbac.Policy{
		Roles: map[string]map[string][]rbac.Grant{
			"analyst": {"users": {{ID: "role:analyst#0", Actions: []entity.Action{entity.ActionRead}}}},
		},
		Principals: map[string]rbac.Principal{"user:alice": {Roles: []string{"analyst"}}},
	}
	db := &store.FakeDB{QueryFn: func(_ context.Context, _ string, _ ...any) (store.Rows, error) {
		return store.NewFakeRows([]string{"id"}, []any{int64(1)}), nil
	}}
	auditor := &recorderAuditor{}
	tc := Context{
		Role: "user:alice", User: "alice", UserRoles: []string{"analyst"},
		DB: db, Dialect: testdialect.Postgres{}, Registry: reg,
		Authorizer: rbac.NewGrantAuthorizer(reg, policy), Auditor: auditor,
	}
	input, _ := json.Marshal(readInput{Entity: "users", Fields: []string{"id"}})
	if _, err := RunTool(context.Background(), ReadTool{}, input, tc); err != nil {
		t.Fatal(err)
	}
	if len(auditor.events) != 1 {
		t.Fatalf("events = %+v", auditor.events)
	}
	ev := auditor.events[0]
	if ev.Role != "user:alice" || ev.User != "alice" || !slices.Equal(ev.Roles, []string{"analyst"}) ||
		!slices.Equal(ev.Grants, []string{"role:analyst#0"}) {
		t.Fatalf("audit identity = role %q user %q roles %v grants %v", ev.Role, ev.User, ev.Roles, ev.Grants)
	}
}
