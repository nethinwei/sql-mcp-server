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
