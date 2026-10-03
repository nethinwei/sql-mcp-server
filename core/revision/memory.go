package revision

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// MemoryStore is an in-process Store. It is the reference implementation of
// the state machine and backs tests; it does not persist across restarts.
type MemoryStore struct {
	mu     sync.Mutex
	revs   map[int64]Revision
	nextID int64
	now    func() time.Time
}

// NewMemoryStore returns an empty MemoryStore. A nil now uses time.Now.
func NewMemoryStore(now func() time.Time) *MemoryStore {
	if now == nil {
		now = time.Now
	}
	return &MemoryStore{revs: make(map[int64]Revision), nextID: 1, now: now}
}

func (m *MemoryStore) timestamp() time.Time {
	return m.now().UTC().Truncate(time.Microsecond)
}

// Create implements Store.
func (m *MemoryStore) Create(_ context.Context, d Draft) (Revision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := Revision{
		ID: m.nextID, ParentID: d.ParentID, ContentHash: Hash(d.Payload),
		Payload: append([]byte(nil), d.Payload...), State: StateDraft,
		Author: d.Author, Comment: d.Comment, CreatedAt: m.timestamp(),
	}
	m.revs[r.ID] = r
	m.nextID++
	return cloneRevision(r), nil
}

// Get implements Store.
func (m *MemoryStore) Get(_ context.Context, id int64) (Revision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.revs[id]
	if !ok {
		return Revision{}, fmt.Errorf("%w: revision %d", ErrNotFound, id)
	}
	return cloneRevision(r), nil
}

// List implements Store.
func (m *MemoryStore) List(_ context.Context, limit int) ([]Revision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Revision, 0, len(m.revs))
	for _, r := range m.revs {
		out = append(out, r.Summary())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Published implements Store.
func (m *MemoryStore) Published(_ context.Context) (Revision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.publishedLocked()
	if !ok {
		return Revision{}, ErrNoPublished
	}
	return cloneRevision(r), nil
}

func (m *MemoryStore) publishedLocked() (Revision, bool) {
	for _, r := range m.revs {
		if r.State == StatePublished {
			return r, true
		}
	}
	return Revision{}, false
}

func (m *MemoryStore) checkCurrentLocked(expected int64) (Revision, error) {
	current, _ := m.publishedLocked()
	if current.ID != expected {
		return Revision{}, fmt.Errorf("%w: expected %d, found %d", ErrConflict, expected, current.ID)
	}
	return current, nil
}

// Publish implements Store.
func (m *MemoryStore) Publish(_ context.Context, id, expectedCurrent int64, _ Meta) (Revision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	target, ok := m.revs[id]
	if !ok {
		return Revision{}, fmt.Errorf("%w: revision %d", ErrNotFound, id)
	}
	if target.State != StateDraft {
		return Revision{}, fmt.Errorf("%w: revision %d is %s, only drafts can be published",
			ErrInvalidState, id, target.State)
	}
	current, err := m.checkCurrentLocked(expectedCurrent)
	if err != nil {
		return Revision{}, err
	}
	if current.ID != 0 {
		current.State = StateSuperseded
		m.revs[current.ID] = current
	}
	target.State = StatePublished
	target.PublishedAt = m.timestamp()
	m.revs[id] = target
	return cloneRevision(target), nil
}

// Rollback implements Store.
func (m *MemoryStore) Rollback(_ context.Context, expectedCurrent, target int64, meta Meta) (Revision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if expectedCurrent == 0 {
		return Revision{}, ErrNoPublished
	}
	current, err := m.checkCurrentLocked(expectedCurrent)
	if err != nil {
		return Revision{}, err
	}
	all := make([]Revision, 0, len(m.revs))
	for _, r := range m.revs {
		all = append(all, r)
	}
	source, err := RollbackTarget(all, current, target)
	if err != nil {
		return Revision{}, err
	}
	current.State = StateRolledBack
	m.revs[current.ID] = current
	now := m.timestamp()
	r := Revision{
		ID: m.nextID, ParentID: source.ID, ContentHash: source.ContentHash,
		Payload: append([]byte(nil), source.Payload...), State: StatePublished,
		Author: meta.Author, Comment: meta.Comment, CreatedAt: now, PublishedAt: now,
	}
	m.revs[r.ID] = r
	m.nextID++
	return cloneRevision(r), nil
}

// ImportHistory implements Store.
func (m *MemoryStore) ImportHistory(_ context.Context, revs []Revision) error {
	if err := ValidateHistory(revs); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.revs) > 0 {
		return ErrNotEmpty
	}
	for _, r := range revs {
		m.revs[r.ID] = cloneRevision(r)
		if r.ID >= m.nextID {
			m.nextID = r.ID + 1
		}
	}
	return nil
}

// Close implements Store.
func (m *MemoryStore) Close() error { return nil }

func cloneRevision(r Revision) Revision {
	if r.Payload != nil {
		r.Payload = append([]byte(nil), r.Payload...)
	}
	return r
}
