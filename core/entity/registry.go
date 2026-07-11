package entity

import (
	"errors"
	"fmt"
)

// ErrDuplicateEntity is returned by NewRegistry when two entities share a name.
var ErrDuplicateEntity = errors.New("entity: duplicate name")

// ErrEmptyName is returned by NewRegistry when an entity has an empty name.
var ErrEmptyName = errors.New("entity: empty name")

// Registry is an immutable set of entities keyed by logical name. It holds no
// per-request state and is safe for concurrent use (invariant I9).
type Registry struct {
	byName   map[string]Entity
	entities []Entity
}

// NewRegistry builds a Registry from entities, rejecting empty names and
// duplicates. It copies the slice so callers cannot mutate the registry.
func NewRegistry(entities []Entity) (*Registry, error) {
	r := &Registry{
		byName:   make(map[string]Entity, len(entities)),
		entities: make([]Entity, 0, len(entities)),
	}
	for _, e := range entities {
		if e.Name == "" {
			return nil, fmt.Errorf("%w", ErrEmptyName)
		}
		if _, ok := r.byName[e.Name]; ok {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateEntity, e.Name)
		}
		r.byName[e.Name] = e
		r.entities = append(r.entities, e)
	}
	return r, nil
}

// Resolve returns the entity by logical name with field projection applied
// (excluded attributes removed). The found flag is false if no such entity.
func (r *Registry) Resolve(name string) (Resolved, bool) {
	e, ok := r.byName[name]
	if !ok {
		return Resolved{}, false
	}
	attrs := make([]Attribute, 0, len(e.Attributes))
	for _, a := range e.Attributes {
		if !a.Excluded {
			attrs = append(attrs, a)
		}
	}
	return Resolved{Entity: e, Attributes: attrs}, true
}

// Entities returns all registered entities.
func (r *Registry) Entities() []Entity {
	out := make([]Entity, len(r.entities))
	copy(out, r.entities)
	return out
}
