package bootstrap

import (
	"slices"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
)

func TestTableCapability(t *testing.T) {
	t.Parallel()
	g, d := introspect.PrivilegeGranted, introspect.PrivilegeDenied
	readOnly := introspect.TablePrivileges{Select: g, Insert: d, Update: d, Delete: d}
	noSelect := introspect.TablePrivileges{Select: d, Insert: g, Update: g, Delete: g}
	columns := introspect.TablePrivileges{Select: d, Insert: d, Update: d, Delete: d,
		Columns: map[string][]string{"select": {"id"}, "update": {"name"}}}
	for name, tc := range map[string]struct {
		tp      introspect.TablePrivileges
		action  entity.Action
		want    introspect.Privilege
		columns []string
	}{
		"select granted":               {readOnly, entity.ActionRead, g, nil},
		"insert denied":                {readOnly, entity.ActionCreate, d, nil},
		"update without select":        {noSelect, entity.ActionUpdate, d, nil},
		"delete without select":        {noSelect, entity.ActionDelete, d, nil},
		"insert without select":        {noSelect, entity.ActionCreate, g, nil},
		"column-level update":          {columns, entity.ActionUpdate, g, []string{"name"}},
		"column-level select filtered": {columns, entity.ActionRead, g, []string{"id"}},
	} {
		c := tableCapability(Capability{}, tc.tp, tc.action)
		if c.Privilege != tc.want || !slices.Equal(c.Columns, tc.columns) {
			t.Errorf("%s: %+v", name, c)
		}
	}
}

func TestCapabilityWarnings(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Entities: []config.EntityConfig{{Name: "orders", Roles: config.RoleConfig{Read: []string{"a"}}}},
		Roles: map[string]config.RoleDefinition{"clerk": {Grants: []config.GrantConfig{
			{Entity: "orders", Actions: []string{"update"}},
		}}},
	}
	caps := EntityCapabilities{"default.orders": {
		entity.ActionRead:   {Privilege: introspect.PrivilegeGranted, Connection: "ro"},
		entity.ActionUpdate: {Privilege: introspect.PrivilegeDenied, Connection: "ro", Reason: "read-only"},
		entity.ActionDelete: {Privilege: introspect.PrivilegeDenied, Connection: "ro"},
	}}
	warnings := CapabilityWarnings(cfg, caps)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "update") || !strings.Contains(warnings[0], `"ro"`) {
		t.Fatalf("warnings = %v, want only the granted update", warnings)
	}
}
