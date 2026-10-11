package configapp

import (
	"context"
	"slices"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/internal/kit"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/configrefs"
)

// FieldReference is a place in the workspace naming a field of an entity:
// a role or user (Via fields: a grant lists it, which removing the field
// takes care of; rows: a grant filters by it, to fix first), or an entity's
// relationship, primaryKey, uniqueKey or tenantPolicy (Name its ID).
type FieldReference struct {
	Field string  `json:"field"`
	Kind  string  `json:"kind"`
	Name  string  `json:"name"`
	Via   *string `json:"via,omitempty"`
}

// FieldReferences are the places the caller's workspace names the fields of
// the entity with ID entity, by field: roles and users first, a grant's row
// filter before its field lists.
func (r *Service) FieldReferences(ctx context.Context, entity string) ([]FieldReference, error) {
	who, err := auth.RequireUnlimited(ctx, auth.PermRead)
	if err != nil {
		return nil, err
	}
	ops, err := r.workspaces()
	if err != nil {
		return nil, err
	}
	st, ok, err := ops.Read(ctx, who.Username)
	if err != nil || !ok {
		return []FieldReference{}, err
	}
	sections, err := sectionsOfItems(st.Workspace.Items)
	if err != nil {
		return nil, err
	}
	return fieldReferencesOf(sections, entity), nil
}

var fieldReferenceOrder = []string{"role", "user", "relationship", "primaryKey", "uniqueKey", "tenantPolicy"}

func fieldReferencesOf(s Sections, id string) []FieldReference {
	entities := make([]config.EntityConfig, 0, len(s.Entities))
	for _, e := range s.Entities {
		entities = append(entities, config.EntityConfig{
			Name: e.Name, DataSource: kit.Deref(e.Datasource), Schema: kit.Deref(e.Schema),
		})
	}
	c := fieldRefs{index: configrefs.NewIndex(entities), id: id, out: []FieldReference{}}
	for _, r := range s.Roles {
		c.grants("role", r.Name, r.Grants)
	}
	for _, u := range s.Users {
		c.grants("user", u.Name, u.Grants)
	}
	for i, e := range s.Entities {
		c.entity(entities[i].ID(), e)
	}
	slices.SortStableFunc(c.out, func(a, b FieldReference) int {
		return slices.Index(fieldReferenceOrder, a.Kind) - slices.Index(fieldReferenceOrder, b.Kind)
	})
	return c.out
}

// fieldRefs collects the references to the fields of the entity id.
type fieldRefs struct {
	index configrefs.Index
	id    string
	out   []FieldReference
}

func (c *fieldRefs) add(entity, field, kind, name, via string) {
	if entity != c.id || slices.ContainsFunc(c.out, func(r FieldReference) bool {
		return r.Field == field && r.Kind == kind && r.Name == name
	}) {
		return
	}
	c.out = append(c.out, FieldReference{Field: field, Kind: kind, Name: name, Via: kit.Optional(via)})
}

// grants are a role's or user's: row filters first, as they block a removal.
func (c *fieldRefs) grants(kind, name string, grants []GrantItem) {
	for _, g := range grants {
		on, _ := c.index.IDOf(g.Entity)
		for _, f := range configrefs.FilterFields(NormalizeJSON(g.Rows)) {
			c.add(on, f, kind, name, "rows")
		}
	}
	for _, g := range grants {
		on, _ := c.index.IDOf(g.Entity)
		for _, f := range slices.Concat(g.ReadFields, g.WriteFields) {
			c.add(on, f, kind, name, "fields")
		}
	}
}

// entity are the references the entity with ID eid makes.
func (c *fieldRefs) entity(eid string, e EntityItem) {
	for _, rel := range e.Relationships {
		joinOn, _ := rel.JoinOn.(map[string]any)
		target, _ := c.index.IDOf(rel.Target)
		for local, remote := range joinOn {
			c.add(eid, local, "relationship", eid, "")
			if field, ok := remote.(string); ok {
				c.add(target, field, "relationship", eid, "")
			}
		}
	}
	for _, f := range e.PrimaryKey {
		c.add(eid, f, "primaryKey", eid, "")
	}
	for _, k := range e.UniqueKeys {
		for _, f := range k {
			c.add(eid, f, "uniqueKey", eid, "")
		}
	}
	for _, f := range configrefs.FilterFields(NormalizeJSON(e.TenantPolicy)) {
		c.add(eid, f, "tenantPolicy", eid, "")
	}
}
