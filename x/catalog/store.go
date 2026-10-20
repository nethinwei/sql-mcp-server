package catalog

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"sync"
	"time"
)

// Store keeps schema lists and scanned schemas per source (by Source.ID).
// Implementations must be safe for concurrent use.
type Store interface {
	// CatalogList returns the kept schema list of source; false when none.
	CatalogList(ctx context.Context, source string) (List, bool, error)
	// PutCatalogList replaces the schema list of source.
	PutCatalogList(ctx context.Context, source, datasource string, l List) error
	// CatalogVersions lists the kept schemas of source without their tables.
	CatalogVersions(ctx context.Context, source string) ([]Version, error)
	// CatalogSchemas returns the kept schemas of source among names.
	CatalogSchemas(ctx context.Context, source string, names []string) ([]Schema, error)
	// PutCatalogSchemas replaces the given schemas of source.
	PutCatalogSchemas(ctx context.Context, source, datasource string, schemas []Schema) error
	// PruneCatalog drops what was kept for datasource from sources other
	// than keep, listed or scanned before before.
	PruneCatalog(ctx context.Context, datasource, keep string, before time.Time) error
	// DeleteCatalogSchemas drops the schemas of source among names scanned
	// before before.
	DeleteCatalogSchemas(ctx context.Context, source string, names []string, before time.Time) error
	BatchStore
}

// MemoryStore is a Store held in process memory, for a server without a
// configuration store and for tests.
type MemoryStore struct {
	mu      sync.Mutex
	lists   map[string]memoryList
	schemas map[string]map[string]memorySchema
	batches memoryBatches
}

type memoryList struct {
	datasource string
	list       List
}

type memorySchema struct {
	datasource string
	schema     Schema
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{lists: map[string]memoryList{}, schemas: map[string]map[string]memorySchema{}}
}

func (m *MemoryStore) CatalogList(_ context.Context, source string) (List, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.lists[source]
	l.list.Schemas = slices.Clone(l.list.Schemas)
	return l.list, ok, nil
}

func (m *MemoryStore) PutCatalogList(_ context.Context, source, datasource string, l List) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	l.Schemas = slices.Clone(l.Schemas)
	m.lists[source] = memoryList{datasource, l}
	return nil
}

func (m *MemoryStore) CatalogVersions(_ context.Context, source string) ([]Version, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Version{}
	for name, s := range m.schemas[source] {
		out = append(out, Version{Name: name, ScannedAt: s.schema.ScannedAt, Tables: len(s.schema.Tables)})
	}
	slices.SortFunc(out, func(a, b Version) int { return cmp.Compare(a.Name, b.Name) })
	return out, nil
}

func (m *MemoryStore) CatalogSchemas(_ context.Context, source string, names []string) ([]Schema, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Schema{}
	for _, name := range names {
		if s, ok := m.schemas[source][name]; ok {
			out = append(out, cloneSchema(s.schema))
		}
	}
	return out, nil
}

func (m *MemoryStore) PutCatalogSchemas(_ context.Context, source, datasource string, schemas []Schema) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.schemas[source] == nil {
		m.schemas[source] = map[string]memorySchema{}
	}
	for _, s := range schemas {
		m.schemas[source][s.Name] = memorySchema{datasource, cloneSchema(s)}
	}
	return nil
}

// cloneSchema copies s through JSON, as a store that serializes it does:
// what is kept and what callers hold share nothing.
func cloneSchema(s Schema) Schema {
	data, err := json.Marshal(s)
	var out Schema
	if err == nil && json.Unmarshal(data, &out) == nil {
		out.Name, out.ScannedAt = s.Name, s.ScannedAt
		return out
	}
	return s
}

func (m *MemoryStore) PruneCatalog(_ context.Context, datasource, keep string, before time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for source, l := range m.lists {
		if source != keep && l.datasource == datasource && l.list.ListedAt.Before(before) {
			delete(m.lists, source)
		}
	}
	for source, schemas := range m.schemas {
		for name, s := range schemas {
			if source != keep && s.datasource == datasource && s.schema.ScannedAt.Before(before) {
				delete(schemas, name)
			}
		}
	}
	return nil
}

func (m *MemoryStore) DeleteCatalogSchemas(_ context.Context, source string, names []string, before time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, name := range names {
		if s, ok := m.schemas[source][name]; ok && s.schema.ScannedAt.Before(before) {
			delete(m.schemas[source], name)
		}
	}
	return nil
}

var _ Store = (*MemoryStore)(nil)
