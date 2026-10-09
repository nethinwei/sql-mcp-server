package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"time"

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

// assessTimeout bounds the background assessment of capabilities.
const assessTimeout = 2 * time.Minute

// assessInBackground assesses the capabilities of the App's entities off the
// assembly path: they only inform administrators, so serving does not wait
// for the privilege queries. Close stops it.
func (a *App) assessInBackground(cfg *config.Config, connections Connections, entities []entity.Entity) {
	ctx, cancel := context.WithTimeout(context.Background(), assessTimeout)
	a.stopAssessing, a.assessed = cancel, make(chan struct{})
	go func() {
		defer close(a.assessed)
		defer cancel()
		capabilities := assessCapabilities(ctx, cfg, connections, entities)
		if ctx.Err() != nil {
			return // closed, or timed out: the capabilities stay unknown
		}
		a.capabilities.Store(&capabilities)
		for _, warning := range CapabilityWarnings(cfg, capabilities) {
			slog.Warn("grant exceeds the datasource connection's privileges", "detail", warning)
		}
	}()
}

// Capabilities returns what the routed connections may do per entity action,
// as the databases reported after the App was assembled; nil (unknown) while
// they are being assessed.
func (a *App) Capabilities() EntityCapabilities {
	if c := a.capabilities.Load(); c != nil {
		return *c
	}
	return nil
}

// WaitCapabilities waits for the assessment of capabilities, or for ctx.
func (a *App) WaitCapabilities(ctx context.Context) (EntityCapabilities, error) {
	if a.assessed != nil {
		select {
		case <-a.assessed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return a.Capabilities(), nil
}

// connectionInspector caches what one connection reports.
type connectionInspector struct {
	name     string
	inspect  introspect.PrivilegeInspector
	readOnly *bool
	tables   map[string]introspect.TablePrivileges // by schema + "." + table
	errs     map[string]error                      // by schema
}

func (c *connectionInspector) isReadOnly(ctx context.Context) bool {
	if c.readOnly == nil {
		readOnly, err := c.inspect.ReadOnly(ctx)
		c.readOnly = new(err == nil && readOnly)
	}
	return *c.readOnly
}

// load reads the privileges on the tables of each schema, a schema at a time.
func (c *connectionInspector) load(ctx context.Context, bySchema map[string][]string) {
	if c.inspect == nil {
		return
	}
	for schema, tables := range bySchema {
		found, err := c.inspect.TablePrivileges(ctx, schema, tables)
		if err != nil {
			c.errs[schema] = err
			continue
		}
		for table, p := range found {
			c.tables[schema+"."+table] = p
		}
	}
}

func (c *connectionInspector) table(schema, table string) (introspect.TablePrivileges, error) {
	if err := c.errs[schema]; err != nil {
		return introspect.TablePrivileges{}, err
	}
	p, ok := c.tables[schema+"."+table]
	if !ok {
		return p, fmt.Errorf("the connection cannot see %s", table)
	}
	return p, nil
}

// routedConnection is the connection an entity action runs on.
func routedConnection(route config.RoutingConfig, action entity.Action) string {
	switch action {
	case entity.ActionCreate, entity.ActionUpdate, entity.ActionDelete:
		return route.Write
	case entity.ActionExecute:
		return route.Execute
	}
	return route.Read
}

// assessCapabilities asks each routed connection what it may do for each
// entity action, reading table privileges a schema at a time.
func assessCapabilities(
	ctx context.Context, cfg *config.Config, connections Connections, entities []entity.Entity,
) EntityCapabilities {
	inspectors := map[string]*connectionInspector{}
	inspector := func(e entity.Entity, action entity.Action) *connectionInspector {
		datasource := e.DatasourceName()
		name := routedConnection(cfg.Databases[datasource].Route(), action)
		key := datasource + "/" + name
		if c, ok := inspectors[key]; ok {
			return c
		}
		c := &connectionInspector{name: name, tables: map[string]introspect.TablePrivileges{}, errs: map[string]error{}}
		if p := connections[datasource][name]; p != nil {
			c.inspect, _ = p.Introspector().(introspect.PrivilegeInspector)
		}
		inspectors[key] = c
		return c
	}
	type table struct {
		c              *connectionInspector
		schema, source string
	}
	seen := map[table]bool{}
	wanted := map[*connectionInspector]map[string][]string{}
	for _, e := range entities {
		for _, action := range entity.ActionsFor(e.Kind) {
			c := inspector(e, action)
			if t := (table{c, e.Schema, e.Source}); action != entity.ActionExecute && !seen[t] {
				seen[t] = true
				if wanted[c] == nil {
					wanted[c] = map[string][]string{}
				}
				wanted[c][e.Schema] = append(wanted[c][e.Schema], e.Source)
			}
		}
	}
	for c, bySchema := range wanted {
		c.load(ctx, bySchema)
	}
	out := make(EntityCapabilities, len(entities))
	for _, e := range entities {
		out[e.Name] = map[entity.Action]Capability{}
		for _, action := range entity.ActionsFor(e.Kind) {
			out[e.Name][action] = assessAction(ctx, inspector(e, action), e, action)
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
	tp, err := c.table(e.Schema, e.Source)
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

// grantedActions collects the actions any role or user is granted per entity
// ID.
func grantedActions(cfg *config.Config) map[string][]entity.Action {
	refs := config.NewEntityRefs(cfg.Entities)
	out := map[string][]entity.Action{}
	add := func(entityName, action string) {
		if a, ok := configActions[action]; ok && !slices.Contains(out[entityName], a) {
			out[entityName] = append(out[entityName], a)
		}
	}
	grants := func(gs []config.GrantConfig) {
		for _, g := range gs {
			for _, a := range g.Actions {
				add(entityID(refs, g.Entity), a)
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
				add(e.ID(), action)
			}
		}
	}
	return out
}
