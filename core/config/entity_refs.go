package config

import (
	"errors"
	"fmt"
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
	byID  map[string]EntityConfig
	byRef map[string][]EntityConfig
}

// NewEntityRefs indexes entities; the last of several with one ID wins.
func NewEntityRefs(entities []EntityConfig) EntityRefs {
	r := EntityRefs{byID: map[string]EntityConfig{}, byRef: map[string][]EntityConfig{}}
	for _, e := range entities {
		r.byID[e.ID()] = e
		for _, ref := range entity.ReferencesOf(e.DataSource, e.Schema, e.Name) {
			r.byRef[ref] = append(r.byRef[ref], e)
		}
	}
	return r
}

// Resolve returns the one entity ref names.
func (r EntityRefs) Resolve(ref string) (EntityConfig, error) {
	if e, ok := r.byID[ref]; ok {
		return e, nil
	}
	switch matches := r.byRef[ref]; len(matches) {
	case 0:
		return EntityConfig{}, fmt.Errorf("%w %q", ErrUnknownEntity, ref)
	case 1:
		return matches[0], nil
	default:
		ids := make([]string, len(matches))
		for i, e := range matches {
			ids[i] = e.ID()
		}
		return EntityConfig{}, fmt.Errorf("%w %q: qualify it as one of %s", ErrAmbiguousEntity, ref,
			strings.Join(ids, ", "))
	}
}
