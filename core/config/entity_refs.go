package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/entity"
)

var (
	// ErrUnknownEntity reports an entity reference naming no entity.
	ErrUnknownEntity = errors.New("unknown entity")
	// ErrAmbiguousEntity reports an entity reference naming several entities.
	ErrAmbiguousEntity = errors.New("ambiguous entity")
)

// ID is the canonical identity of the entity (see entity.ID).
func (e EntityConfig) ID() string { return entity.ID(e.DataSource, e.Schema, e.Name) }

// EntityRefs resolves entity references the way the gateway does (see
// entity.Registry.Match): an entity's ID names it, otherwise any of its
// entity.ReferencesOf. A configuration's references must each name exactly
// one entity, so adding an entity can never silently change what an existing
// reference names: the configuration fails validation instead.
type EntityRefs struct {
	// entities are the entities indexed, a copy (the caller may change its
	// own later); the maps hold positions in it, not copies of each.
	entities []EntityConfig
	byID     map[string]int
	byRef    map[string][]int
}

// NewEntityRefs indexes entities; the last of several with one ID wins.
func NewEntityRefs(entities []EntityConfig) EntityRefs {
	r := EntityRefs{entities: slices.Clone(entities), byID: make(map[string]int, len(entities)),
		byRef: make(map[string][]int, 2*len(entities))}
	for i, e := range r.entities {
		r.byID[e.ID()] = i
		for _, ref := range entity.ReferencesOf(e.DataSource, e.Schema, e.Name) {
			r.byRef[ref] = append(r.byRef[ref], i)
		}
	}
	return r
}

// Resolve returns the one entity ref names.
func (r EntityRefs) Resolve(ref string) (EntityConfig, error) {
	if i, ok := r.byID[ref]; ok {
		return r.entities[i], nil
	}
	switch matches := r.byRef[ref]; len(matches) {
	case 0:
		return EntityConfig{}, fmt.Errorf("%w %q", ErrUnknownEntity, ref)
	case 1:
		return r.entities[matches[0]], nil
	default:
		ids := make([]string, len(matches))
		for n, i := range matches {
			ids[n] = r.entities[i].ID()
		}
		return EntityConfig{}, fmt.Errorf("%w %q: qualify it as one of %s", ErrAmbiguousEntity, ref,
			strings.Join(ids, ", "))
	}
}
