package bootstrap

import (
	"context"
	"fmt"
	"slices"
	"sort"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
)

// Capability says whether the connection an entity action routes to may run
// it, as far as the database reports (I-3). It informs administrators; the
// database decides each statement.
type Capability struct {
	Privilege  introspect.Privilege
	Connection string
	// Columns lists the columns granted at column level when the table-level
	// privilege is not granted.
	Columns []string
	Reason  string
}

// EntityCapabilities are capabilities by entity and action.
type EntityCapabilities map[string]map[entity.Action]Capability

// connectionInspector caches what one connection reports.
type connectionInspector struct {
	name     string
	inspect  introspect.PrivilegeInspector
	readOnly *bool
	tables   map[string]introspect.TablePrivileges
}

func (c *connectionInspector) isReadOnly(ctx context.Context) bool {
	if c.readOnly == nil {
		readOnly, err := c.inspect.ReadOnly(ctx)
		c.readOnly = new(err == nil && readOnly)
	}
	return *c.readOnly
}

func (c *connectionInspector) table(ctx context.Context, schema, table string) (introspect.TablePrivileges, error) {
	key := schema + "." + table
	if p, ok := c.tables[key]; ok {
		return p, nil
	}
	p, err := c.inspect.TablePrivileges(ctx, schema, table)
	if err == nil {
		c.tables[key] = p
	}
	return p, err
}

// assessCapabilities asks each routed connection what it may do for each
// entity action.
func assessCapabilities(cfg *config.Config, connections Connections, entities []entity.Entity) EntityCapabilities {
	ctx := context.Background()
	inspectors := map[string]*connectionInspector{}
	inspector := func(datasource, name string) *connectionInspector {
		key := datasource + "/" + name
		if c, ok := inspectors[key]; ok {
			return c
		}
		c := &connectionInspector{name: name, tables: map[string]introspect.TablePrivileges{}}
		if p := connections[datasource][name]; p != nil {
			c.inspect, _ = p.Introspector().(introspect.PrivilegeInspector)
		}
		inspectors[key] = c
		return c
	}
	out := make(EntityCapabilities, len(entities))
	for _, e := range entities {
		route := cfg.Databases[e.DatasourceName()].Route()
		out[e.Name] = map[entity.Action]Capability{}
		for _, action := range entity.ActionsFor(e.Kind) {
			name := route.Read
			switch action {
			case entity.ActionCreate, entity.ActionUpdate, entity.ActionDelete:
				name = route.Write
			case entity.ActionExecute:
				name = route.Execute
			}
			out[e.Name][action] = assessAction(ctx, inspector(e.DatasourceName(), name), e, action)
		}
	}
	return out
}

func assessAction(ctx context.Context, c *connectionInspector, e entity.Entity, action entity.Action) Capability {
	capability := Capability{Connection: c.name}
	if c.inspect == nil {
		return capability
	}
	writes := action != entity.ActionRead && action != entity.ActionAggregate
	if writes && action != entity.ActionExecute && c.isReadOnly(ctx) {
		capability.Privilege, capability.Reason = introspect.PrivilegeDenied, "the server is read-only"
		return capability
	}
	if action == entity.ActionExecute {
		p, err := c.inspect.ProcedurePrivilege(ctx, e.Schema, e.Source)
		capability.Privilege = p
		if err != nil {
			capability.Reason = err.Error()
		}
		return capability
	}
	tp, err := c.table(ctx, e.Schema, e.Source)
	if err != nil {
		capability.Reason = err.Error()
		return capability
	}
	return tableCapability(capability, tp, action)
}

// tableCapability maps table privileges to an action. An UPDATE or DELETE
// with a filter also needs SELECT on the filtered columns.
func tableCapability(c Capability, tp introspect.TablePrivileges, action entity.Action) Capability {
	privilege, kind := tp.Select, "select"
	switch action {
	case entity.ActionCreate:
		privilege, kind = tp.Insert, "insert"
	case entity.ActionUpdate:
		privilege, kind = tp.Update, "update"
	case entity.ActionDelete:
		privilege, kind = tp.Delete, ""
	}
	c.Privilege = privilege
	if privilege == introspect.PrivilegeDenied && len(tp.Columns[kind]) > 0 {
		c.Privilege, c.Columns = introspect.PrivilegeGranted, tp.Columns[kind]
	}
	if c.Privilege == introspect.PrivilegeDenied {
		c.Reason = fmt.Sprintf("the account lacks the %s privilege", privilegeName(action))
	}
	filters := action == entity.ActionUpdate || action == entity.ActionDelete
	if filters && c.Privilege != introspect.PrivilegeDenied && tp.Select == introspect.PrivilegeDenied &&
		len(tp.Columns["select"]) == 0 {
		c.Privilege, c.Reason = introspect.PrivilegeDenied, "a filtered "+privilegeName(action)+" also needs SELECT"
	}
	return c
}

func privilegeName(action entity.Action) string {
	return map[entity.Action]string{
		entity.ActionRead: "SELECT", entity.ActionAggregate: "SELECT", entity.ActionCreate: "INSERT",
		entity.ActionUpdate: "UPDATE", entity.ActionDelete: "DELETE", entity.ActionExecute: "EXECUTE",
	}[action]
}

// CapabilityWarnings lists actions cfg grants that the routed connection
// cannot run, in a stable order.
func CapabilityWarnings(cfg *config.Config, capabilities EntityCapabilities) []string {
	var out []string
	for entityName, actions := range grantedActions(cfg) {
		for _, action := range actions {
			c, ok := capabilities[entityName][action]
			if ok && c.Privilege == introspect.PrivilegeDenied {
				out = append(out, fmt.Sprintf("entity %q: %s is granted, but connection %q cannot run it (%s)",
					entityName, action, c.Connection, c.Reason))
			}
		}
	}
	sort.Strings(out)
	return out
}

// grantedActions collects the actions any role or user is granted per entity.
func grantedActions(cfg *config.Config) map[string][]entity.Action {
	out := map[string][]entity.Action{}
	add := func(entityName, action string) {
		if a, ok := configActions[action]; ok && !slices.Contains(out[entityName], a) {
			out[entityName] = append(out[entityName], a)
		}
	}
	grants := func(gs []config.GrantConfig) {
		for _, g := range gs {
			for _, a := range g.Actions {
				add(g.Entity, a)
			}
		}
	}
	for _, r := range cfg.Roles {
		grants(r.Grants)
	}
	for _, u := range cfg.Users {
		grants(u.Grants)
	}
	for _, e := range cfg.Entities {
		for action, roles := range map[string][]string{
			"read": e.Roles.Read, "create": e.Roles.Create, "update": e.Roles.Update,
			"delete": e.Roles.Delete, "execute": e.Roles.Execute, "aggregate": e.Roles.Aggregate,
		} {
			if len(roles) > 0 {
				add(e.Name, action)
			}
		}
	}
	return out
}
