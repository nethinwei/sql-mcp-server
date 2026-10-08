package accounts

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/nethinwei/sql-mcp-server/x/configstore"
)

// MemoryStore is an in-process Store with the same semantics as the SQL
// store, for tests and embedded use.
type MemoryStore struct {
	mu       sync.Mutex
	accounts map[string]Account
	now      func() time.Time
}

// NewMemoryStore returns a store holding seed.
func NewMemoryStore(seed ...Account) *MemoryStore {
	m := &MemoryStore{accounts: map[string]Account{}, now: time.Now}
	for _, a := range seed {
		m.accounts[a.Username] = a
	}
	return m
}

// GetAdmin implements Store.
func (m *MemoryStore) GetAdmin(_ context.Context, username string) (Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.accounts[username]
	if !ok {
		return Account{}, fmt.Errorf("%w: %q", configstore.ErrAdminNotFound, username)
	}
	return a, nil
}

// ListAdmins implements Store.
func (m *MemoryStore) ListAdmins(context.Context) ([]Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sorted(), nil
}

func (m *MemoryStore) sorted() []Account {
	out := make([]Account, 0, len(m.accounts))
	for _, a := range m.accounts {
		a.Permissions = slices.Clone(a.Permissions)
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out
}

// MutateAdmins implements Store.
func (m *MemoryStore) MutateAdmins(_ context.Context, fn func([]Account) ([]Account, error)) ([]Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	changed, err := fn(m.sorted())
	if err != nil {
		return nil, err
	}
	now := m.now()
	for i, a := range changed {
		if old, ok := m.accounts[a.Username]; ok {
			a.CreatedAt = old.CreatedAt
		} else {
			a.CreatedAt = now
		}
		a.UpdatedAt = now
		m.accounts[a.Username] = a
		changed[i] = a
	}
	return changed, nil
}
