// Package configrefs holds the rules for references among a configuration's
// entities as entities change: what an existing reference names afterwards
// and how it is written. The console's edits (x/admin/configapp) and plans
// (x/configplan) apply the same rules, each over its own representation.
//
// The rule: a reference keeps naming the entity it named before the change,
// in its shortest unambiguous form when it no longer names it alone; a
// reference to a removed entity goes; one that named no single entity before
// stays as it was.
package configrefs

import (
	"maps"
	"slices"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
)

// Index resolves references among a set of entities.
type Index struct {
	refs config.EntityRefs
	byID map[string]config.EntityConfig
}

// NewIndex indexes entities; only their names, datasources and schemas
// matter.
func NewIndex(entities []config.EntityConfig) Index {
	x := Index{refs: config.NewEntityRefs(entities), byID: make(map[string]config.EntityConfig, len(entities))}
	for _, e := range entities {
		x.byID[e.ID()] = e
	}
	return x
}

// Has reports whether an entity has the ID id.
func (x Index) Has(id string) bool {
	_, ok := x.byID[id]
	return ok
}

// IDOf is the ID of the one entity ref names.
func (x Index) IDOf(ref string) (string, bool) {
	e, err := x.refs.Resolve(ref)
	if err != nil {
		return "", false
	}
	return e.ID(), true
}

// ShortName is the shortest reference naming the entity id alone (its ID
// when none does, or when there is no such entity).
func (x Index) ShortName(id string) string {
	e, ok := x.byID[id]
	if !ok {
		return id
	}
	refs := entity.ReferencesOf(e.DataSource, e.Schema, e.Name)
	for _, ref := range refs {
		if named, ok := x.IDOf(ref); ok && named == id {
			return ref
		}
	}
	return refs[len(refs)-1]
}

// Retarget is what the existing reference ref becomes once the entities
// changed from before to after; moved maps the IDs of renamed or moved
// entities to their new IDs, and of removed ones to "" (needed when another
// entity takes over the ID). keep is false when ref named an entity that is
// gone: the reference goes with it.
func Retarget(ref string, before, after Index, moved map[string]string) (out string, keep bool) {
	was, ok := before.IDOf(ref)
	if !ok {
		return ref, true
	}
	now := was
	if to, ok := moved[was]; ok {
		now = to
	}
	if now == "" || !after.Has(now) {
		return "", false
	}
	if named, ok := after.IDOf(ref); ok && named == now {
		return ref, true
	}
	return after.ShortName(now), true
}

// FilterFields lists the fields a row filter (decoded YAML or JSON) names:
// a condition {op, field, value} names its field, an and/or those of its
// items, and a not those of its filter.
func FilterFields(v any) []string {
	f, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	if _, ok := f["op"]; ok {
		if field, ok := f["field"].(string); ok {
			return []string{field}
		}
		return nil
	}
	if inner, ok := f["not"]; ok {
		return FilterFields(inner)
	}
	var out []string
	for _, key := range []string{"and", "or"} {
		items, _ := f[key].([]any)
		for _, item := range items {
			out = append(out, FilterFields(item)...)
		}
	}
	return out
}

// The kinds of FieldUse: what in an entity's definition names a field.
const (
	UsePrimaryKey   = "primaryKey"
	UseUniqueKey    = "uniqueKey"
	UseTenantPolicy = "tenantPolicy"
	UseFieldACL     = "fieldACL"  // entity-level, by role
	UseRowPolicy    = "rowPolicy" // entity-level, by role
	UseRelationship = "relationship"
)

// FieldUse is a field an entity's definition names: one of its own, or the
// target's a relationship joins on.
type FieldUse struct {
	// Entity is the ID of the entity whose field it is.
	Entity, Field, Kind string
	// Relationship names the relationship, for UseRelationship.
	Relationship string
}

// FieldUses calls fn for every field e's definition names, in one order:
// its relationships' join columns on both sides (the target as resolve finds
// it, none when it names no single entity), its keys, its tenant policy,
// and its entity-level access. Grants, which name fields of the entities
// they are on, are their own.
func FieldUses(e config.EntityConfig, resolve func(ref string) (string, bool), fn func(FieldUse)) {
	id := e.ID()
	for _, r := range e.Relationships {
		target, ok := resolve(r.Target)
		for _, local := range slices.Sorted(maps.Keys(r.JoinOn)) {
			fn(FieldUse{Entity: id, Field: local, Kind: UseRelationship, Relationship: r.Name})
			if ok {
				fn(FieldUse{Entity: target, Field: r.JoinOn[local], Kind: UseRelationship, Relationship: r.Name})
			}
		}
	}
	for _, f := range e.PrimaryKey {
		fn(FieldUse{Entity: id, Field: f, Kind: UsePrimaryKey})
	}
	for _, k := range e.UniqueKeys {
		for _, f := range k {
			fn(FieldUse{Entity: id, Field: f, Kind: UseUniqueKey})
		}
	}
	for _, f := range FilterFields(e.TenantPolicy) {
		fn(FieldUse{Entity: id, Field: f, Kind: UseTenantPolicy})
	}
	for _, role := range slices.Sorted(maps.Keys(e.FieldACL)) {
		for _, f := range slices.Concat(e.FieldACL[role].Read, e.FieldACL[role].Write) {
			fn(FieldUse{Entity: id, Field: f, Kind: UseFieldACL})
		}
	}
	for _, role := range slices.Sorted(maps.Keys(e.RowPolicies)) {
		for _, f := range FilterFields(e.RowPolicies[role]) {
			fn(FieldUse{Entity: id, Field: f, Kind: UseRowPolicy})
		}
	}
}
