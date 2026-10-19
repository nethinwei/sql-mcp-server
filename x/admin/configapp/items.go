package configapp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/internal/kit"
	"github.com/nethinwei/sql-mcp-server/x/configedit"
	"github.com/nethinwei/sql-mcp-server/x/configyaml"
)

// Sections are a configuration as a workspace holds it, item by item: each
// entity, role and user, and the settings (every other top-level section).
// Their JSON is what the workspace stores and the console edits; the admin
// API's input types (EntityInput, RoleInput, UserInput) have the same shape.
// A nil list or settings is a section left out (an edit keeps the base's).
type Sections struct {
	Entities []EntityItem   `json:"entities"`
	Roles    []RoleItem     `json:"roles"`
	Users    []UserItem     `json:"users"`
	Settings map[string]any `json:"settings"`
}

// EntityItem is an entity. Lists are always written (empty when unset);
// legacyAccess holds the entity-level roles, fieldACL and rowPolicies.
type EntityItem struct {
	Name          string             `json:"name"`
	Source        *string            `json:"source,omitempty"`
	Datasource    *string            `json:"datasource,omitempty"`
	Schema        *string            `json:"schema,omitempty"`
	Kind          *string            `json:"kind,omitempty"`
	Description   *string            `json:"description,omitempty"`
	PrimaryKey    []string           `json:"primaryKey"`
	UniqueKeys    [][]string         `json:"uniqueKeys"`
	Fields        []FieldItem        `json:"fields"                 merge:"key=name"`
	Relationships []RelationshipItem `json:"relationships"          merge:"key=name"`
	TenantPolicy  any                `json:"tenantPolicy,omitempty"`
	LegacyAccess  any                `json:"legacyAccess,omitempty"`
	Mcp           *EntityMCPItem     `json:"mcp,omitempty"`
	Params        []string           `json:"params"`
	Affects       []string           `json:"affects"`
	AllowCascade  *bool              `json:"allowCascade,omitempty" merge:"unset"`
	// AllowKeyUpdate lets update_records assign the row's identifying columns.
	AllowKeyUpdate *bool `json:"allowKeyUpdate,omitempty" merge:"unset"`
}

// FieldItem is a field of an entity.
type FieldItem struct {
	Name        string  `json:"name"`
	Alias       *string `json:"alias,omitempty"`
	Description *string `json:"description,omitempty"`
	Mask        *string `json:"mask,omitempty"`
	Exclude     *bool   `json:"exclude,omitempty"     merge:"unset"`
}

// RelationshipItem is a relationship of an entity.
type RelationshipItem struct {
	Name        string `json:"name"`
	Target      string `json:"target"`
	Cardinality string `json:"cardinality"`
	JoinOn      any    `json:"joinOn"`
}

// EntityMCPItem are an entity's MCP flags; an unset dmlTools defaults on.
type EntityMCPItem struct {
	DmlTools         *bool `json:"dmlTools,omitempty"`
	CustomTool       *bool `json:"customTool,omitempty"       merge:"unset"`
	TrustedProcedure *bool `json:"trustedProcedure,omitempty" merge:"unset"`
}

// GrantItem is a grant of a role or user; actions are upper case (READ).
// Without fieldsRestricted, read or write fields restrict the grant.
type GrantItem struct {
	Entity           string   `json:"entity"`
	Actions          []string `json:"actions"`
	FieldsRestricted *bool    `json:"fieldsRestricted,omitempty"`
	ReadFields       []string `json:"readFields"`
	WriteFields      []string `json:"writeFields"`
	Rows             any      `json:"rows,omitempty"`
}

// RoleItem is a role. Permissions are its system permissions
// (sql:execute@<datasource>).
type RoleItem struct {
	Name        string      `json:"name"`
	Description *string     `json:"description,omitempty"`
	Grants      []GrantItem `json:"grants"                merge:"key=entity"`
	Permissions []string    `json:"permissions"`
	// Limits are config.PrincipalLimits as the configuration writes them
	// (durations as text), as settings are.
	Limits any `json:"limits,omitempty"`
}

// UserItem is a user, its token included: one without a token hash has none.
type UserItem struct {
	Name        string      `json:"name"`
	Description *string     `json:"description,omitempty"`
	Roles       []string    `json:"roles"`
	Grants      []GrantItem `json:"grants"                merge:"key=entity"`
	Permissions []string    `json:"permissions"`
	Subject     any         `json:"subject,omitempty"`
	Disabled    *bool       `json:"disabled,omitempty"    merge:"unset"`
	TokenHash   *string     `json:"tokenHash,omitempty"`
	// CertSubjects name the client certificates that are the user (mTLS).
	CertSubjects []string `json:"certSubjects"`
	// Limits are as a role's.
	Limits any `json:"limits,omitempty"`
}

// structuredSections are the top-level sections Sections hold as items;
// every other one is part of the settings.
var structuredSections = []string{"database", "databases", "entities", "roles", "users"}

// SectionsOf are the sections of what a configuration says: an unset value
// is not in its item (lists are written empty).
func SectionsOf(spec *config.Spec) (Sections, error) {
	payload, err := configyaml.EncodeSpec(spec)
	if err != nil {
		return Sections{}, err
	}
	doc := map[string]any{}
	if err := yaml.Unmarshal(payload, &doc); err != nil {
		return Sections{}, fmt.Errorf("decode payload: %w", err)
	}
	s := Sections{
		Entities: make([]EntityItem, 0, len(spec.Entities)), Roles: []RoleItem{}, Users: []UserItem{},
		Settings: map[string]any{},
	}
	for k, v := range doc {
		if !slices.Contains(structuredSections, k) {
			s.Settings[k] = v
		}
	}
	for _, e := range spec.Entities {
		s.Entities = append(s.Entities, entityItem(e))
	}
	for _, name := range kit.SortedKeys(spec.Roles) {
		def := spec.Roles[name]
		s.Roles = append(s.Roles, RoleItem{
			Name: name, Description: nonEmpty(def.Description), Grants: grantItems(def.Grants),
			Permissions: listOf([]string(def.Permissions)), Limits: limitsOf(doc, "roles", name),
		})
	}
	for _, name := range kit.SortedKeys(spec.Users) {
		u := spec.Users[name]
		item := UserItem{
			Name: name, Description: nonEmpty(u.Description), Roles: listOf([]string(u.Roles)),
			Grants: grantItems(u.Grants), Permissions: listOf([]string(u.Permissions)), Disabled: u.Disabled,
			TokenHash: nonEmpty(u.TokenHash), CertSubjects: listOf([]string(u.CertSubjects)),
			Limits: limitsOf(doc, "users", name),
		}
		if len(u.Subject) > 0 {
			item.Subject = u.Subject
		}
		s.Users = append(s.Users, item)
	}
	return s, nil
}

// settingsKeys are the top-level sections spec writes that the settings
// item holds.
func settingsKeys(spec *config.Spec) []string {
	v := reflect.ValueOf(spec).Elem()
	t := v.Type()
	var out []string
	for i := range t.NumField() {
		name := config.YAMLName(t.Field(i))
		if name != "" && !v.Field(i).IsZero() && !slices.Contains(structuredSections, name) {
			out = append(out, name)
		}
	}
	return out
}

// SectionsOfPayload are the sections of a stored payload.
func SectionsOfPayload(payload []byte) (Sections, error) {
	spec, err := configyaml.DecodeSpec(payload)
	if err != nil {
		return Sections{}, err
	}
	return SectionsOf(spec)
}

// SectionsOfConfig are the sections of a configuration built in code (a
// plan's outcome), as SectionsOf has them: a zero value is unset.
func SectionsOfConfig(cfg *config.Config) (Sections, error) { return SectionsOf(config.SpecOf(cfg)) }

// EntityItemOf is the item of an entity built in code (a candidate).
func EntityItemOf(e config.EntityConfig) EntityItem {
	return entityItem(config.SpecOf(&config.Config{Entities: []config.EntityConfig{e}}).Entities[0])
}

func entityItem(e config.EntitySpec) EntityItem {
	out := EntityItem{
		Name: kit.Deref(
			e.Name,
		), Source: nonEmpty(e.Source), Datasource: nonEmpty(e.DataSource), Schema: nonEmpty(e.Schema),
		Kind: nonEmpty(e.Kind), Description: nonEmpty(e.Description), PrimaryKey: listOf([]string(e.PrimaryKey)),
		UniqueKeys: listOf([][]string(e.UniqueKeys)), Params: listOf([]string(e.Params)),
		Affects: listOf([]string(e.Affects)), AllowCascade: e.AllowCascade,
		AllowKeyUpdate: e.AllowKeyUpdate,
		Fields:         make([]FieldItem, 0, len(e.Fields)), Relationships: make([]RelationshipItem, 0, len(e.Relationships)),
	}
	if e.MCP != nil {
		out.Mcp = &EntityMCPItem{
			DmlTools: e.MCP.DMLTools, CustomTool: e.MCP.CustomTool, TrustedProcedure: e.MCP.TrustedProcedure,
		}
	}
	for _, f := range e.Fields {
		out.Fields = append(out.Fields, FieldItem{
			Name: kit.Deref(
				f.Name,
			), Alias: nonEmpty(f.Alias), Description: nonEmpty(f.Description), Mask: nonEmpty(f.Mask),
			Exclude: f.Exclude,
		})
	}
	for _, r := range e.Relationships {
		joinOn := make(map[string]any, len(r.JoinOn))
		for k, v := range r.JoinOn {
			joinOn[k] = v
		}
		out.Relationships = append(out.Relationships, RelationshipItem{
			Name: kit.Deref(r.Name), Target: kit.Deref(r.Target), Cardinality: kit.Deref(r.Cardinality), JoinOn: joinOn,
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
func legacyAccess(e config.EntitySpec) map[string]any {
	out := map[string]any{}
	roles := map[string]any{}
	if e.Roles != nil {
		for action, list := range map[string]config.List[string]{
			"read": e.Roles.Read, "create": e.Roles.Create, "update": e.Roles.Update,
			"delete": e.Roles.Delete, "execute": e.Roles.Execute, "aggregate": e.Roles.Aggregate,
		} {
			if len(list) > 0 {
				roles[action] = []string(list)
			}
		}
	}
	if len(roles) > 0 {
		out["roles"] = roles
	}
	if len(e.FieldACL) > 0 {
		acl := map[string]any{}
		for role, a := range e.FieldACL {
			acl[role] = map[string]any{"read": listOf([]string(a.Read)), "write": listOf([]string(a.Write))}
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

func grantItems(gs config.List[config.GrantSpec]) []GrantItem {
	out := make([]GrantItem, 0, len(gs))
	for _, g := range gs {
		item := GrantItem{Entity: kit.Deref(g.Entity), Actions: make([]string, 0, len(g.Actions)),
			ReadFields: []string{}, WriteFields: []string{}}
		for _, a := range g.Actions {
			item.Actions = append(item.Actions, strings.ToUpper(a))
		}
		restricted := g.Fields != nil
		item.FieldsRestricted = &restricted
		if restricted {
			item.ReadFields, item.WriteFields = listOf([]string(g.Fields.Read)), listOf([]string(g.Fields.Write))
		}
		if g.Rows != nil {
			item.Rows = g.Rows
		}
		out = append(out, item)
	}
	return out
}

// Edit is s as an edit of a base configuration, saying what the items say:
// the sections s holds replace the base's, empty text and lists, and
// switches off unless set, are left unset (they mean the same), and a
// user's token is the one its item holds (none without a hash).
func (s Sections) Edit() (configedit.Edit, error) {
	var edit configedit.Edit
	if s.Entities != nil {
		edit.Entities = make([]config.EntitySpec, 0, len(s.Entities))
		for _, e := range s.Entities {
			es, err := e.spec()
			if err != nil {
				return configedit.Edit{}, err
			}
			edit.Entities = append(edit.Entities, es)
		}
	}
	if s.Roles != nil {
		edit.Roles = make([]configedit.Role, 0, len(s.Roles))
		for _, r := range s.Roles {
			grants, err := grantSpecs(r.Grants)
			if err != nil {
				return configedit.Edit{}, fmt.Errorf("role %q: %w", r.Name, err)
			}
			limits, err := limitsSpec(r.Limits)
			if err != nil {
				return configedit.Edit{}, fmt.Errorf("role %q: %w", r.Name, err)
			}
			edit.Roles = append(edit.Roles, configedit.Role{Name: r.Name, RoleDefinitionSpec: config.RoleDefinitionSpec{
				Description: nonEmpty(r.Description), Grants: grants, Permissions: listSpec(r.Permissions), Limits: limits,
			}})
		}
	}
	if s.Users != nil {
		edit.Users = make([]configedit.User, 0, len(s.Users))
		for _, u := range s.Users {
			user, err := u.spec()
			if err != nil {
				return configedit.Edit{}, fmt.Errorf("user %q: %w", u.Name, err)
			}
			edit.Users = append(edit.Users, user)
		}
	}
	if s.Settings != nil {
		settings, ok := NormalizeJSON(map[string]any(s.Settings)).(map[string]any)
		if !ok {
			return configedit.Edit{}, errors.New("settings must be an object of top-level sections")
		}
		edit.Settings = settings
	}
	return edit, nil
}

func (in EntityItem) spec() (config.EntitySpec, error) {
	e := config.EntitySpec{
		Name: &in.Name, Source: nonEmpty(in.Source), DataSource: nonEmpty(in.Datasource), Schema: nonEmpty(in.Schema),
		Kind: nonEmpty(in.Kind), Description: nonEmpty(in.Description), PrimaryKey: listSpec(in.PrimaryKey),
		UniqueKeys: listSpec(in.UniqueKeys), Params: listSpec(in.Params), Affects: listSpec(in.Affects),
		AllowCascade: onlyTrue(in.AllowCascade), AllowKeyUpdate: onlyTrue(in.AllowKeyUpdate),
	}
	for _, f := range in.Fields {
		e.Fields = append(e.Fields, config.FieldSpec{
			Name: kit.Ptr(
				f.Name,
			), Alias: nonEmpty(f.Alias), Description: nonEmpty(f.Description), Mask: nonEmpty(f.Mask),
			Exclude: onlyTrue(f.Exclude),
		})
	}
	for _, r := range in.Relationships {
		joinOn, err := decodeJSON[map[string]string](r.JoinOn)
		if err != nil {
			return config.EntitySpec{}, fmt.Errorf("entity %q relationship %q joinOn: %w", in.Name, r.Name, err)
		}
		e.Relationships = append(e.Relationships, config.RelationshipSpec{
			Name: kit.Ptr(r.Name), Target: kit.Ptr(r.Target), Cardinality: kit.Ptr(r.Cardinality), JoinOn: joinOn,
		})
	}
	if in.TenantPolicy != nil {
		policy, ok := NormalizeJSON(in.TenantPolicy).(map[string]any)
		if !ok {
			return config.EntitySpec{}, fmt.Errorf("entity %q tenantPolicy must be an object", in.Name)
		}
		e.TenantPolicy = policy
	}
	if err := in.legacySpec(&e); err != nil {
		return config.EntitySpec{}, err
	}
	if in.Mcp != nil {
		mcp := config.MCPFlagsSpec{
			DMLTools: in.Mcp.DmlTools, CustomTool: onlyTrue(in.Mcp.CustomTool),
			TrustedProcedure: onlyTrue(in.Mcp.TrustedProcedure),
		}
		if mcp != (config.MCPFlagsSpec{}) {
			e.MCP = &mcp
		}
	}
	return e, nil
}

// legacySpec sets the entity-level access legacyAccess holds on e.
func (in EntityItem) legacySpec(e *config.EntitySpec) error {
	if in.LegacyAccess == nil {
		return nil
	}
	legacy, err := decodeJSON[struct {
		Roles       *config.RoleSpec                        `json:"roles"`
		FieldACL    config.Map[string, config.FieldACLSpec] `json:"fieldACL"`
		RowPolicies config.Map[string, map[string]any]      `json:"rowPolicies"`
	}](in.LegacyAccess)
	if err != nil {
		return fmt.Errorf("entity %q legacyAccess: %w", in.Name, err)
	}
	e.Roles, e.FieldACL, e.RowPolicies = legacy.Roles, legacy.FieldACL, legacy.RowPolicies
	return nil
}

func grantSpecs(gs []GrantItem) (config.List[config.GrantSpec], error) {
	out := make(config.List[config.GrantSpec], 0, len(gs))
	for _, g := range gs {
		actions := make(config.List[string], 0, len(g.Actions))
		for _, a := range g.Actions {
			actions = append(actions, strings.ToLower(a))
		}
		gs := config.GrantSpec{Entity: kit.Ptr(g.Entity), Actions: actions}
		restricted := g.ReadFields != nil || g.WriteFields != nil
		if g.FieldsRestricted != nil {
			restricted = *g.FieldsRestricted
		}
		if restricted {
			gs.Fields = &config.FieldACLSpec{Read: listSpec(g.ReadFields), Write: listSpec(g.WriteFields)}
		}
		if g.Rows != nil {
			rows, ok := NormalizeJSON(g.Rows).(map[string]any)
			if !ok {
				return nil, fmt.Errorf("grant on %q: rows must be an object", g.Entity)
			}
			gs.Rows = rows
		}
		out = append(out, gs)
	}
	return out, nil
}

func (u UserItem) spec() (configedit.User, error) {
	grants, err := grantSpecs(u.Grants)
	if err != nil {
		return configedit.User{}, err
	}
	limits, err := limitsSpec(u.Limits)
	if err != nil {
		return configedit.User{}, err
	}
	out := configedit.User{Name: u.Name, UserSpec: config.UserSpec{
		Description: nonEmpty(u.Description), TokenHash: nonEmpty(u.TokenHash), CertSubjects: listSpec(u.CertSubjects),
		Roles:  listSpec(u.Roles),
		Grants: grants, Permissions: listSpec(u.Permissions), Disabled: onlyTrue(u.Disabled), Limits: limits,
	}}
	if u.Subject != nil {
		subject, ok := NormalizeJSON(u.Subject).(map[string]any)
		if !ok {
			return configedit.User{}, errors.New("subject must be an object")
		}
		out.Subject = subject
	}
	return out, nil
}

// limitsOf are the limits the configuration document writes for the role or
// user name (section roles or users); nil when none.
func limitsOf(doc map[string]any, section, name string) any {
	byName, _ := doc[section].(map[string]any)
	principal, _ := byName[name].(map[string]any)
	if limits, ok := principal["limits"].(map[string]any); ok && len(limits) > 0 {
		return limits
	}
	return nil
}

// limitsSpec reads an item's limits as the configuration does: unknown
// fields and wrong types fail.
func limitsSpec(v any) (*config.PrincipalLimitsSpec, error) {
	limits, ok := NormalizeJSON(v).(map[string]any)
	if v == nil || ok && len(limits) == 0 {
		return nil, nil
	}
	if !ok {
		return nil, errors.New("limits must be an object")
	}
	data, err := yaml.Marshal(limits)
	if err != nil {
		return nil, err
	}
	var out config.PrincipalLimitsSpec
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("limits: %w", err)
	}
	return &out, nil
}

// nonEmpty is s as a Spec and an item hold it: nil when empty (empty text
// is unset).
func nonEmpty(s *string) *string { return kit.Optional(kit.Deref(s)) }

// onlyTrue is a switch off unless set as a Spec holds it: nil unless on.
func onlyTrue(b *bool) *bool {
	if b == nil || !*b {
		return nil
	}
	return b
}

// listSpec is a list as a Spec holds it: nil when empty.
func listSpec[T any](l []T) config.List[T] {
	if len(l) == 0 {
		return nil
	}
	return config.List[T](l)
}

// NormalizeJSON turns json.Number values (GraphQL variables decode numbers
// so) into int64 or float64, so YAML encodes them as numbers, not strings.
func NormalizeJSON(v any) any {
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
			out[k] = NormalizeJSON(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = NormalizeJSON(val)
		}
		return out
	}
	return v
}

// decodeJSON converts a JSON value into T, rejecting unknown fields.
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

// listOf returns s, or an empty list: items always write their lists.
func listOf[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
