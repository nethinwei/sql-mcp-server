package graph

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/configedit"
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
		out = append(out, Datasource{Name: name, Driver: db.Driver, Dsn: bootstrap.RedactDSN(db.Driver, db.DSN)})
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
		Name: e.Name, Source: optional(e.Source), Datasource: optional(e.DataSource), Schema: optional(e.Schema),
		Kind:        optional(e.Kind),
		Description: optional(e.Description), PrimaryKey: orEmpty(e.PrimaryKey), Params: orEmpty(e.Params),
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

// toEdit maps a draft input to a configuration edit. Every input field must
// reach the edit; graph_test's round trip fails for one that does not.
func toEdit(in DraftInput) (configedit.Edit, error) {
	var edit configedit.Edit
	if in.Entities != nil {
		edit.Entities = make([]config.EntityConfig, 0, len(in.Entities))
		for _, e := range in.Entities {
			ec, err := entityConfig(e)
			if err != nil {
				return configedit.Edit{}, err
			}
			edit.Entities = append(edit.Entities, ec)
		}
	}
	if in.Roles != nil {
		edit.Roles = make([]configedit.Role, 0, len(in.Roles))
		for _, r := range in.Roles {
			grants, err := grantConfigs(r.Grants)
			if err != nil {
				return configedit.Edit{}, fmt.Errorf("role %q: %w", r.Name, err)
			}
			edit.Roles = append(edit.Roles, configedit.Role{Name: r.Name, RoleDefinition: config.RoleDefinition{
				Description: deref(r.Description), Grants: grants,
			}})
		}
	}
	if in.Users != nil {
		edit.Users = make([]configedit.User, 0, len(in.Users))
		for _, u := range in.Users {
			user, err := userConfig(u)
			if err != nil {
				return configedit.Edit{}, fmt.Errorf("user %q: %w", u.Name, err)
			}
			edit.Users = append(edit.Users, user)
		}
	}
	if in.Settings != nil {
		settings, ok := normalizeJSON(in.Settings).(map[string]any)
		if !ok {
			return configedit.Edit{}, errors.New("settings must be an object of top-level sections")
		}
		edit.Settings = settings
	}
	return edit, nil
}

func entityConfig(in EntityInput) (config.EntityConfig, error) {
	e := config.EntityConfig{
		Name: in.Name, Source: deref(in.Source), DataSource: deref(in.Datasource), Schema: deref(in.Schema),
		Kind: deref(in.Kind), Description: deref(in.Description), PrimaryKey: in.PrimaryKey, Params: in.Params,
	}
	for _, f := range in.Fields {
		e.Fields = append(e.Fields, config.FieldConfig{
			Name: f.Name, Alias: deref(f.Alias), Description: deref(f.Description), Mask: deref(f.Mask),
			Exclude: derefBool(f.Exclude),
		})
	}
	for _, r := range in.Relationships {
		joinOn, err := decodeJSON[map[string]string](r.JoinOn)
		if err != nil {
			return config.EntityConfig{}, fmt.Errorf("entity %q relationship %q joinOn: %w", in.Name, r.Name, err)
		}
		e.Relationships = append(e.Relationships, config.RelationshipConfig{
			Name: r.Name, Target: r.Target, Cardinality: r.Cardinality, JoinOn: joinOn,
		})
	}
	if in.TenantPolicy != nil {
		policy, ok := normalizeJSON(in.TenantPolicy).(map[string]any)
		if !ok {
			return config.EntityConfig{}, fmt.Errorf("entity %q tenantPolicy must be an object", in.Name)
		}
		e.TenantPolicy = policy
	}
	if in.LegacyAccess != nil {
		legacy, err := decodeJSON[struct {
			Roles       config.RoleConfig                `json:"roles"`
			FieldACL    map[string]config.FieldACLConfig `json:"fieldACL"`
			RowPolicies config.RowPolicies               `json:"rowPolicies"`
		}](in.LegacyAccess)
		if err != nil {
			return config.EntityConfig{}, fmt.Errorf("entity %q legacyAccess: %w", in.Name, err)
		}
		e.Roles, e.FieldACL, e.RowPolicies = legacy.Roles, legacy.FieldACL, legacy.RowPolicies
	}
	if in.Mcp != nil {
		// An explicit dmlTools differs from an unset one, which defaults on.
		if in.Mcp.DmlTools != nil {
			e.MCP = config.MCPFlagsWithDMLTools(*in.Mcp.DmlTools)
		}
		e.MCP.CustomTool, e.MCP.TrustedProcedure = derefBool(in.Mcp.CustomTool), derefBool(in.Mcp.TrustedProcedure)
	}
	return e, nil
}

func grantConfigs(gs []GrantInput) ([]config.GrantConfig, error) {
	out := make([]config.GrantConfig, 0, len(gs))
	for _, g := range gs {
		actions := make([]string, 0, len(g.Actions))
		for _, a := range g.Actions {
			actions = append(actions, strings.ToLower(string(a)))
		}
		gc := config.GrantConfig{Entity: g.Entity, Actions: actions}
		restricted := g.ReadFields != nil || g.WriteFields != nil
		if g.FieldsRestricted != nil {
			restricted = *g.FieldsRestricted
		}
		if restricted {
			gc.Fields = &config.FieldACLConfig{Read: orEmpty(g.ReadFields), Write: orEmpty(g.WriteFields)}
		}
		if g.Rows != nil {
			rows, ok := normalizeJSON(g.Rows).(map[string]any)
			if !ok {
				return nil, fmt.Errorf("grant on %q: rows must be an object", g.Entity)
			}
			gc.Rows = rows
		}
		out = append(out, gc)
	}
	return out, nil
}

// userConfig maps a user input; a user without tokenHash keeps the token of
// the same-named base user.
func userConfig(u UserInput) (configedit.User, error) {
	grants, err := grantConfigs(u.Grants)
	if err != nil {
		return configedit.User{}, err
	}
	out := configedit.User{Name: u.Name, KeepToken: u.TokenHash == nil, UserConfig: config.UserConfig{
		Description: deref(u.Description), TokenHash: deref(u.TokenHash), Roles: u.Roles, Grants: grants,
		Disabled: derefBool(u.Disabled),
	}}
	if u.Subject != nil {
		subject, ok := normalizeJSON(u.Subject).(map[string]any)
		if !ok {
			return configedit.User{}, errors.New("subject must be an object")
		}
		out.Subject = subject
	}
	return out, nil
}

// decodeJSON converts a JSON scalar value into T, rejecting unknown fields.
func decodeJSON[T any](v any) (T, error) {
	var out T
	data, err := json.Marshal(v)
	if err != nil {
		return out, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	err = dec.Decode(&out)
	return out, err
}
