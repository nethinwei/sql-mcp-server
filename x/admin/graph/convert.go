package graph

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
)

// structuredKeys are the top-level sections exposed as structured types;
// every other section is part of Configuration.settings.
var structuredKeys = []string{"database", "databases", "entities", "roles", "users"}

// toConfiguration builds the read model of a configuration. payload is its
// YAML, the source of the settings sections, read in the current encoding.
func toConfiguration(cfg *config.Config, payload []byte) (*Configuration, error) {
	doc, err := yamlDocument(reencode(payload))
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
	databases := cfg.Databases
	if len(databases) == 0 && cfg.Database.Driver != "" {
		databases = map[string]config.DatabaseConfig{"default": cfg.Database}
	}
	out := make([]Datasource, 0, len(databases))
	for name, db := range databases {
		out = append(out, Datasource{Name: name, Driver: db.Driver, Dsn: bootstrap.RedactDSN(db.DSN)})
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

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func toEntity(e config.EntityConfig) Entity {
	out := Entity{
		Name: e.Name, Source: e.Source, Datasource: e.DataSource, Schema: optional(e.Schema), Kind: e.Kind,
		Description: optional(e.Description), PrimaryKey: orEmpty(e.PrimaryKey), Params: orEmpty(e.Params),
		Fields:        make([]Field, 0, len(e.Fields)),
		Relationships: make([]Relationship, 0, len(e.Relationships)),
		Mcp: &EntityMcp{
			DmlTools: e.MCP.DMLTools, CustomTool: e.MCP.CustomTool, TrustedProcedure: e.MCP.TrustedProcedure,
		},
	}
	if out.Source == "" {
		out.Source = e.Name
	}
	if out.Datasource == "" {
		out.Datasource = "default"
	}
	if out.Kind == "" {
		out.Kind = "table"
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
			Disabled: u.Disabled, HasToken: u.TokenHash != "",
		}
		if len(u.Subject) > 0 {
			user.Subject = u.Subject
		}
		out = append(out, user)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// normalizeJSON turns json.Number values from GraphQL variables into int64
// or float64, so YAML encodes them as numbers rather than strings.
func normalizeJSON(v any) any {
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		f, _ := x.Float64()
		return f
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = normalizeJSON(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = normalizeJSON(val)
		}
		return out
	}
	return v
}

// entityDoc renders an entity input as a YAML document node, writing only
// the keys the client set so presence-sensitive defaults keep their meaning.
func entityDoc(in EntityInput) (map[string]any, error) {
	doc := map[string]any{"name": in.Name}
	for key, val := range map[string]*string{
		"source": in.Source, "datasource": in.Datasource, "schema": in.Schema,
		"kind": in.Kind, "description": in.Description,
	} {
		if val != nil {
			doc[key] = *val
		}
	}
	if in.PrimaryKey != nil {
		doc["primaryKey"] = in.PrimaryKey
	}
	if in.Params != nil {
		doc["params"] = in.Params
	}
	if in.Fields != nil {
		fields := make([]any, 0, len(in.Fields))
		for _, f := range in.Fields {
			fields = append(fields, fieldDoc(f))
		}
		doc["fields"] = fields
	}
	if in.Relationships != nil {
		rels := make([]any, 0, len(in.Relationships))
		for _, r := range in.Relationships {
			rels = append(rels, map[string]any{
				"name": r.Name, "target": r.Target, "cardinality": r.Cardinality, "joinOn": normalizeJSON(r.JoinOn),
			})
		}
		doc["relationships"] = rels
	}
	if in.TenantPolicy != nil {
		doc["tenantPolicy"] = normalizeJSON(in.TenantPolicy)
	}
	return doc, entityAccessAndMCPDoc(doc, in)
}

// entityAccessAndMCPDoc adds legacyAccess keys and the mcp flags to doc.
func entityAccessAndMCPDoc(doc map[string]any, in EntityInput) error {
	if in.LegacyAccess != nil {
		legacy, ok := normalizeJSON(in.LegacyAccess).(map[string]any)
		if !ok {
			return fmt.Errorf("entity %q legacyAccess must be an object", in.Name)
		}
		for key, val := range legacy {
			if key != "roles" && key != "fieldACL" && key != "rowPolicies" {
				return fmt.Errorf("entity %q legacyAccess has unknown key %q", in.Name, key)
			}
			doc[key] = val
		}
	}
	if in.Mcp != nil {
		mcp := map[string]any{}
		for key, val := range map[string]*bool{
			"dmlTools": in.Mcp.DmlTools, "customTool": in.Mcp.CustomTool, "trustedProcedure": in.Mcp.TrustedProcedure,
		} {
			if val != nil {
				mcp[key] = *val
			}
		}
		doc["mcp"] = mcp
	}
	return nil
}

func fieldDoc(f FieldInput) map[string]any {
	doc := map[string]any{"name": f.Name}
	for key, val := range map[string]*string{"alias": f.Alias, "description": f.Description, "mask": f.Mask} {
		if val != nil {
			doc[key] = *val
		}
	}
	if f.Exclude != nil {
		doc["exclude"] = *f.Exclude
	}
	return doc
}

func grantDoc(g GrantInput) map[string]any {
	actions := make([]string, 0, len(g.Actions))
	for _, a := range g.Actions {
		actions = append(actions, strings.ToLower(string(a)))
	}
	doc := map[string]any{"entity": g.Entity, "actions": actions}
	restricted := g.ReadFields != nil || g.WriteFields != nil
	if g.FieldsRestricted != nil {
		restricted = *g.FieldsRestricted
	}
	if restricted {
		doc["fields"] = map[string]any{"read": orEmpty(g.ReadFields), "write": orEmpty(g.WriteFields)}
	}
	if g.Rows != nil {
		doc["rows"] = normalizeJSON(g.Rows)
	}
	return doc
}

func grantDocs(gs []GrantInput) []any {
	out := make([]any, 0, len(gs))
	for _, g := range gs {
		out = append(out, grantDoc(g))
	}
	return out
}

func roleDocs(roles []RoleInput) (map[string]any, error) {
	out := make(map[string]any, len(roles))
	for _, r := range roles {
		if _, dup := out[r.Name]; dup {
			return nil, fmt.Errorf("role %q listed twice", r.Name)
		}
		doc := map[string]any{"grants": grantDocs(r.Grants)}
		if r.Description != nil {
			doc["description"] = *r.Description
		}
		out[r.Name] = doc
	}
	return out, nil
}

// userDocs renders users; a user without tokenHash keeps the hash of the
// same-named user in base.
func userDocs(users []UserInput, base map[string]config.UserConfig) (map[string]any, error) {
	out := make(map[string]any, len(users))
	for _, u := range users {
		if _, dup := out[u.Name]; dup {
			return nil, fmt.Errorf("user %q listed twice", u.Name)
		}
		doc := map[string]any{"roles": orEmpty(u.Roles), "grants": grantDocs(u.Grants)}
		if u.Description != nil {
			doc["description"] = *u.Description
		}
		if u.Subject != nil {
			doc["subject"] = normalizeJSON(u.Subject)
		}
		if u.Disabled != nil {
			doc["disabled"] = *u.Disabled
		}
		switch {
		case u.TokenHash != nil:
			doc["tokenHash"] = *u.TokenHash
		case base[strings.ToLower(u.Name)].TokenHash != "":
			doc["tokenHash"] = base[strings.ToLower(u.Name)].TokenHash
		}
		out[u.Name] = doc
	}
	return out, nil
}
