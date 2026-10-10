package graph

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/revisionops"
)

// structuredKeys are the top-level sections exposed as structured types;
// every other section is part of Configuration.settings.
var structuredKeys = []string{"database", "databases", "entities", "roles", "users"}

// toConfiguration builds the read model of a configuration. payload is its
// YAML, the source of the settings sections, read in the current encoding.
func toConfiguration(cfg *config.Config, payload []byte) (*Configuration, error) {
	doc, err := yamlDocument(revisionops.Reencode(payload))
	if err != nil {
		return nil, err
	}
	settings := make(map[string]any, len(doc))
	for k, v := range doc {
		if !slices.Contains(structuredKeys, k) {
			settings[k] = v
		}
	}
	out := &Configuration{
		Datasources: toDatasources(cfg),
		Entities:    make([]Entity, 0, len(cfg.Entities)),
		Roles:       toRoles(cfg),
		Users:       toUsers(cfg),
		Settings:    settings,
	}
	for _, e := range cfg.Entities {
		out.Entities = append(out.Entities, toEntity(e))
	}
	return out, nil
}

func yamlDocument(payload []byte) (map[string]any, error) {
	doc := map[string]any{}
	if err := yaml.Unmarshal(payload, &doc); err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}
	return doc, nil
}

func toDatasources(cfg *config.Config) []Datasource {
	out := make([]Datasource, 0, len(cfg.Databases))
	for name, db := range cfg.Databases {
		connections := db.ConnectionsOrDSN()
		route := db.Route()
		ds := Datasource{
			Name: name, Driver: db.Driver, Dsn: bootstrap.RedactDSN(db.Driver, connections[route.Read].DSN),
			Routing:     &DatasourceRouting{Read: route.Read, Write: route.Write, Execute: route.Execute},
			Connections: make([]DatasourceConnection, 0, len(connections)),
		}
		if db.ReadAfterWrite > 0 {
			ds.ReadAfterWrite = optional(db.ReadAfterWrite.String())
		}
		for _, connection := range slices.Sorted(maps.Keys(connections)) {
			c := connections[connection]
			role := c.Role
			if role == "" {
				role = "primary"
			}
			ds.Connections = append(ds.Connections, DatasourceConnection{
				Name: connection, Dsn: bootstrap.RedactDSN(db.Driver, c.DSN), Role: role, Pooler: optional(c.Pooler),
			})
		}
		out = append(out, ds)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// optional maps unset text to null, matching the omitempty encoding.
func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// orEmpty returns s, or an empty list for a non-null API field.
func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func toEntity(e config.EntityConfig) Entity {
	out := Entity{
		ID: e.ID(), Name: e.Name, Source: optional(e.Source), Datasource: optional(e.DataSource), Schema: optional(e.Schema),
		Kind:        optional(e.Kind),
		Description: optional(e.Description), PrimaryKey: orEmpty(e.PrimaryKey),
		UniqueKeys: orEmpty(e.UniqueKeys), Params: orEmpty(e.Params),
		Affects: orEmpty(e.Affects), AllowCascade: e.AllowCascade,
		Fields:        make([]Field, 0, len(e.Fields)),
		Relationships: make([]Relationship, 0, len(e.Relationships)),
		Mcp: &EntityMcp{
			DmlTools: e.MCP.DMLTools, CustomTool: e.MCP.CustomTool, TrustedProcedure: e.MCP.TrustedProcedure,
		},
	}
	for _, f := range e.Fields {
		out.Fields = append(out.Fields, Field{
			Name: f.Name, Alias: optional(f.Alias), Description: optional(f.Description), Mask: optional(f.Mask),
			Exclude: f.Exclude,
		})
	}
	for _, r := range e.Relationships {
		joinOn := make(map[string]any, len(r.JoinOn))
		for k, v := range r.JoinOn {
			joinOn[k] = v
		}
		out.Relationships = append(out.Relationships, Relationship{
			Name: r.Name, Target: r.Target, Cardinality: r.Cardinality, JoinOn: joinOn,
		})
	}
	if e.TenantPolicy != nil {
		out.TenantPolicy = e.TenantPolicy
	}
	if legacy := legacyAccess(e); len(legacy) > 0 {
		out.LegacyAccess = legacy
	}
	return out
}

// legacyAccess returns the non-empty entity-level roles, fieldACL and
// rowPolicies in their YAML shape.
func legacyAccess(e config.EntityConfig) map[string]any {
	out := map[string]any{}
	roles := map[string]any{}
	for action, list := range map[string][]string{
		"read": e.Roles.Read, "create": e.Roles.Create, "update": e.Roles.Update,
		"delete": e.Roles.Delete, "execute": e.Roles.Execute, "aggregate": e.Roles.Aggregate,
	} {
		if len(list) > 0 {
			roles[action] = list
		}
	}
	if len(roles) > 0 {
		out["roles"] = roles
	}
	if len(e.FieldACL) > 0 {
		acl := map[string]any{}
		for role, a := range e.FieldACL {
			acl[role] = map[string]any{"read": orEmpty(a.Read), "write": orEmpty(a.Write)}
		}
		out["fieldACL"] = acl
	}
	if len(e.RowPolicies) > 0 {
		policies := map[string]any{}
		for role, p := range e.RowPolicies {
			policies[role] = p
		}
		out["rowPolicies"] = policies
	}
	return out
}

func toGrant(g config.GrantConfig) Grant {
	out := Grant{Entity: g.Entity, Actions: make([]Action, 0, len(g.Actions)),
		ReadFields: []string{}, WriteFields: []string{}}
	for _, a := range g.Actions {
		out.Actions = append(out.Actions, Action(strings.ToUpper(a)))
	}
	if g.Fields != nil {
		out.FieldsRestricted = true
		out.ReadFields, out.WriteFields = orEmpty(g.Fields.Read), orEmpty(g.Fields.Write)
	}
	if g.Rows != nil {
		out.Rows = g.Rows
	}
	return out
}

func toGrants(gs []config.GrantConfig) []Grant {
	out := make([]Grant, 0, len(gs))
	for _, g := range gs {
		out = append(out, toGrant(g))
	}
	return out
}

func toRoles(cfg *config.Config) []Role {
	members := map[string][]string{}
	for name, u := range cfg.Users {
		for _, role := range u.Roles {
			members[role] = append(members[role], name)
		}
	}
	out := make([]Role, 0, len(cfg.Roles))
	for name, def := range cfg.Roles {
		m := orEmpty(members[name])
		sort.Strings(m)
		out = append(out, Role{Name: name, Description: optional(def.Description), Grants: toGrants(def.Grants), Members: m})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func toUsers(cfg *config.Config) []User {
	out := make([]User, 0, len(cfg.Users))
	for name, u := range cfg.Users {
		user := User{
			Name: name, Description: optional(u.Description), Roles: orEmpty(u.Roles), Grants: toGrants(u.Grants),
			Disabled: u.Disabled, HasToken: u.TokenHash != "", TokenHash: optional(u.TokenHash),
		}
		if len(u.Subject) > 0 {
			user.Subject = u.Subject
		}
		out = append(out, user)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
