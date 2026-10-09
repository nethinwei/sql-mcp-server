package catalog

import (
	"cmp"
	"context"
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
			out = append(out, s.schema)
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
		m.schemas[source][s.Name] = memorySchema{datasource, s}
	}
	return nil
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

// Batch is a scan of many schemas of a datasource, run by one process (the
// owner) at a time: the owner records its progress and a heartbeat, and
// another process takes over a batch whose owner stopped beating.
type Batch struct {
	ID         string
	Datasource string
	StartedBy  string
	Owner      string
	StartedAt  time.Time
	FinishedAt time.Time
	Heartbeat  time.Time
	// Canceled asks the owner to stop.
	Canceled bool
	Items    []BatchItem
}

// BatchItem is one schema of a batch: pending, running, done, failed or
// canceled.
type BatchItem struct {
	Schema string `json:"schema"`
	State  string `json:"state"`
	Error  string `json:"error,omitempty"`
}

// BatchStore keeps scan batches.
type BatchStore interface {
	// PutBatch creates or replaces a batch.
	PutBatch(ctx context.Context, b Batch) error
	Batch(ctx context.Context, id string) (Batch, bool, error)
	// Batches lists the batches of datasource, newest first.
	Batches(ctx context.Context, datasource string, limit int) ([]Batch, error)
	// UnfinishedBatches lists every batch not finished.
	UnfinishedBatches(ctx context.Context) ([]Batch, error)
	// ClaimBatch makes owner the owner of an unfinished batch whose heartbeat
	// is before staleBefore, or already owner's, beating at now; false when
	// another owner holds it.
	ClaimBatch(ctx context.Context, id, owner string, staleBefore, now time.Time) (bool, error)
	// CancelBatch asks the owner of a batch to stop.
	CancelBatch(ctx context.Context, id string) error
}

type memoryBatches struct {
	mu  sync.Mutex
	all map[string]Batch
}

func cloneBatch(b Batch) Batch {
	b.Items = slices.Clone(b.Items)
	return b
}

func (m *MemoryStore) PutBatch(_ context.Context, b Batch) error {
	m.batches.mu.Lock()
	defer m.batches.mu.Unlock()
	if m.batches.all == nil {
		m.batches.all = map[string]Batch{}
	}
	if old, ok := m.batches.all[b.ID]; ok && old.Canceled {
		b.Canceled = true // a cancel is never lost to a progress write
	}
	m.batches.all[b.ID] = cloneBatch(b)
	return nil
}

func (m *MemoryStore) Batch(_ context.Context, id string) (Batch, bool, error) {
	m.batches.mu.Lock()
	defer m.batches.mu.Unlock()
	b, ok := m.batches.all[id]
	return cloneBatch(b), ok, nil
}

func (m *MemoryStore) Batches(_ context.Context, datasource string, limit int) ([]Batch, error) {
	m.batches.mu.Lock()
	defer m.batches.mu.Unlock()
	out := []Batch{}
	for _, b := range m.batches.all {
		if b.Datasource == datasource {
			out = append(out, cloneBatch(b))
		}
	}
	slices.SortFunc(
		out,
		func(a, b Batch) int { return cmp.Or(b.StartedAt.Compare(a.StartedAt), cmp.Compare(b.ID, a.ID)) },
	)
	return out[:min(limit, len(out))], nil
}

func (m *MemoryStore) UnfinishedBatches(context.Context) ([]Batch, error) {
	m.batches.mu.Lock()
	defer m.batches.mu.Unlock()
	out := []Batch{}
	for _, b := range m.batches.all {
		if b.FinishedAt.IsZero() {
			out = append(out, cloneBatch(b))
		}
	}
	slices.SortFunc(out, func(a, b Batch) int { return cmp.Compare(a.ID, b.ID) })
	return out, nil
}

func (m *MemoryStore) ClaimBatch(_ context.Context, id, owner string, staleBefore, now time.Time) (bool, error) {
	m.batches.mu.Lock()
	defer m.batches.mu.Unlock()
	b, ok := m.batches.all[id]
	if !ok || !b.FinishedAt.IsZero() || b.Owner != owner && !b.Heartbeat.Before(staleBefore) {
		return false, nil
	}
	b.Owner, b.Heartbeat = owner, now
	m.batches.all[id] = b
	return true, nil
}

func (m *MemoryStore) CancelBatch(_ context.Context, id string) error {
	m.batches.mu.Lock()
	defer m.batches.mu.Unlock()
	if b, ok := m.batches.all[id]; ok {
		b.Canceled = true
		m.batches.all[id] = b
	}
	return nil
}
