package entity

import (
	"errors"
	"fmt"
	"iter"
	"slices"
)

// ErrDuplicateEntity is returned by NewRegistry when two entities share a name.
var ErrDuplicateEntity = errors.New("entity: duplicate name")

// ErrEmptyName is returned by NewRegistry when an entity has an empty name.
var ErrEmptyName = errors.New("entity: empty name")

// Registry is an immutable set of entities keyed by Name, also found by any
// of their References. It holds no per-request state and is safe for
// concurrent use (invariant I9).
type Registry struct {
	byName   map[string]Entity
	byRef    map[string][]string // reference -> names, in registration order
	entities []Entity
	// visible holds each entity's attributes that are not excluded, computed
	// once: every authorization resolves its entity.
	visible map[string][]Attribute
	// procedures are the positions of the procedures among entities: a
	// procedure tool's call finds its procedure among them.
	procedures []int
}

// NewRegistry builds a Registry from entities, rejecting empty names and
// duplicates. It copies the slice so callers cannot mutate the registry.
func NewRegistry(entities []Entity) (*Registry, error) {
	r := &Registry{
		byName:   make(map[string]Entity, len(entities)),
		byRef:    make(map[string][]string, len(entities)),
		entities: make([]Entity, 0, len(entities)),
		visible:  make(map[string][]Attribute, len(entities)),
	}
	for _, e := range entities {
		if e.Name == "" {
			return nil, fmt.Errorf("%w", ErrEmptyName)
		}
		if _, ok := r.byName[e.Name]; ok {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateEntity, e.Name)
		}
		r.byName[e.Name] = e
		for _, ref := range e.References() {
			r.byRef[ref] = append(r.byRef[ref], e.Name)
		}
		if e.Kind == KindProcedure {
			r.procedures = append(r.procedures, len(r.entities))
		}
		r.entities = append(r.entities, e)
		attrs := make([]Attribute, 0, len(e.Attributes))
		for _, a := range e.Attributes {
			if !a.Excluded {
				attrs = append(attrs, a)
			}
		}
		r.visible[e.Name] = slices.Clip(attrs)
	}
	return r, nil
}

// Resolve returns the entity a reference names (see Match) with field
// projection applied (excluded attributes removed). The found flag is false
// if no entity, or more than one, matches.
func (r *Registry) Resolve(ref string) (Resolved, bool) {
	e, ok := r.byName[ref]
	if !ok {
		// Match without building its list: every call resolves its entity.
		names := r.byRef[ref]
		if len(names) != 1 {
			return Resolved{}, false
		}
		e = r.byName[names[0]]
	}
	return Resolved{Entity: e, Attributes: r.visible[e.Name]}, true
}

// Entities returns all registered entities.
func (r *Registry) Entities() []Entity {
	out := make([]Entity, len(r.entities))
	copy(out, r.entities)
	return out
}

// Procedures yields the procedures in registration order without copying
// them.
func (r *Registry) Procedures() iter.Seq[Entity] {
	return func(yield func(Entity) bool) {
		for _, i := range r.procedures {
			if !yield(r.entities[i]) {
				return
			}
		}
	}
}

// All yields the entities in registration order without copying them, for
// the paths that walk every entity per request.
func (r *Registry) All() iter.Seq[Entity] {
	return func(yield func(Entity) bool) {
		for _, e := range r.entities {
			if !yield(e) {
				return
			}
		}
	}
}

// Match returns the entities ref may name: the entity whose Name it is, else
// every entity it is a reference of, like a partially qualified SQL name.
func (r *Registry) Match(ref string) []Entity {
	if e, ok := r.byName[ref]; ok {
		return []Entity{e}
	}
	out := make([]Entity, 0, len(r.byRef[ref]))
	for _, name := range r.byRef[ref] {
		out = append(out, r.byName[name])
	}
	return out
}

// ShortName is the shortest reference naming e alone among the entities
// visible accepts (all when nil); e itself is assumed visible.
func (r *Registry) ShortName(e Entity, visible func(Entity) bool) string {
	refs := e.References()
	for _, ref := range refs[:len(refs)-1] {
		if r.uniqueAmong(ref, e.Name, visible) {
			return ref
		}
	}
	return refs[len(refs)-1]
}

func (r *Registry) uniqueAmong(ref, name string, visible func(Entity) bool) bool {
	if _, ok := r.byName[ref]; ok && ref != name {
		return false
	}
	for _, other := range r.byRef[ref] {
		if other != name && (visible == nil || visible(r.byName[other])) {
			return false
		}
	}
	return true
}
