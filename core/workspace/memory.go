package workspace

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"time"
)

// MemoryStore keeps workspaces in memory, for tests and single-process use.
type MemoryStore struct {
	mu   sync.Mutex
	ws   map[string]Workspace
	logs map[string][]LogEntry
	now  func() time.Time
}

// NewMemoryStore returns an empty MemoryStore. A nil now uses time.Now.
func NewMemoryStore(now func() time.Time) *MemoryStore {
	if now == nil {
		now = time.Now
	}
	return &MemoryStore{ws: map[string]Workspace{}, logs: map[string][]LogEntry{}, now: now}
}

// Workspace implements Store.
func (m *MemoryStore) Workspace(_ context.Context, username string) (Workspace, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w, ok := m.ws[username]
	return copyWorkspace(w), ok, nil
}

// PutWorkspace implements Store.
func (m *MemoryStore) PutWorkspace(_ context.Context, w Workspace, expected int64, entry LogEntry) (Workspace, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ws[w.Username].Version != expected {
		return Workspace{}, ErrChanged
	}
	w.Version = expected + 1
	w.UpdatedAt = m.now().UTC().Truncate(time.Microsecond)
	m.ws[w.Username] = copyWorkspace(w)
	entry.Version, entry.At = w.Version, w.UpdatedAt
	m.logs[w.Username] = append(m.logs[w.Username], entry)
	return w, nil
}

// WorkspaceLog implements Store.
func (m *MemoryStore) WorkspaceLog(_ context.Context, username string, limit int) ([]LogEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := slices.Clone(m.logs[username])
	slices.Reverse(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// copyWorkspace copies w through JSON, as a store that serializes would.
func copyWorkspace(w Workspace) Workspace {
	data, _ := json.Marshal(w)
	var out Workspace
	_ = json.Unmarshal(data, &out)
	return out
}
