package catalog

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"sync"
	"time"
)

// Batch is a scan of many schemas of a datasource, run by one process (the
// owner) at a time: the owner moves each schema through its states and beats,
// and another process takes over a batch whose owner stopped beating. Every
// write of the owner is conditioned on still owning the unfinished batch, in
// the same step as the write, so a run that lost the batch (paused, then
// resumed) writes nothing.
type Batch struct {
	ID         string
	Datasource string
	StartedBy  string
	// RequestKey names the request that started the batch: a request sent
	// again with the same key gets the same batch.
	RequestKey string
	// Owner is one run of the batch, never reused: a takeover makes a new
	// one.
	Owner      string
	StartedAt  time.Time
	FinishedAt time.Time
	Heartbeat  time.Time
	// Canceled asks the owner to stop.
	Canceled bool
	Items    []BatchItem
}

// Item states: an item goes from pending to running, and from running to
// done, failed, back to pending (to try again after NotBefore) or, when the
// batch ends first, canceled.
const (
	ItemPending  = "pending"
	ItemRunning  = "running"
	ItemDone     = "done"
	ItemFailed   = "failed"
	ItemCanceled = "canceled"
)

// BatchItem is one schema of a batch.
type BatchItem struct {
	Schema string
	State  string
	Error  string
	// Attempts counts the scans of the schema started; NotBefore is when a
	// pending one that failed may be scanned again.
	Attempts  int
	NotBefore time.Time
}

// Batch errors.
var (
	// ErrTooManyBatches: the datasource has as many unfinished batches as
	// allowed.
	ErrTooManyBatches = errors.New("catalog: the datasource has too many scan batches running")
	// ErrNotOwner: the batch is finished or another run owns it.
	ErrNotOwner = errors.New("catalog: the scan batch is not this run's")
)

// BatchStore keeps scan batches.
type BatchStore interface {
	// CreateBatch creates b (its owner beating at b.Heartbeat) unless its
	// datasource has maxOpen unfinished batches (ErrTooManyBatches), checking
	// and creating in one step, so concurrent requests, of any process,
	// cannot pass the bound together. A batch started before with b's
	// RequestKey for the datasource is returned instead, with false.
	CreateBatch(ctx context.Context, b Batch, maxOpen int) (Batch, bool, error)
	Batch(ctx context.Context, id string) (Batch, bool, error)
	// Batches lists the batches of datasource, newest first.
	Batches(ctx context.Context, datasource string, limit int) ([]Batch, error)
	// UnfinishedBatches lists every batch not finished.
	UnfinishedBatches(ctx context.Context) ([]Batch, error)
	// ClaimBatch makes owner the owner of an unfinished batch whose heartbeat
	// is before staleBefore, or already owner's, beating at now; false when
	// another owner holds it.
	ClaimBatch(ctx context.Context, id, owner string, staleBefore, now time.Time) (bool, error)
	// MoveItem moves item (by its schema) to its state, error, attempts and
	// NotBefore when it is in one of from, the owner beating at now; false
	// when it is in none of them. ErrNotOwner, writing nothing, when the
	// batch is finished or not owner's.
	MoveItem(ctx context.Context, id, owner string, now time.Time, item BatchItem, from ...string) (bool, error)
	// FinishBatch ends a batch owner owns at at, canceling its pending and
	// running items in the same step; ErrNotOwner when it does not own it.
	FinishBatch(ctx context.Context, id, owner string, at time.Time) error
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

func (m *MemoryStore) CreateBatch(_ context.Context, b Batch, maxOpen int) (Batch, bool, error) {
	m.batches.mu.Lock()
	defer m.batches.mu.Unlock()
	open := 0
	for _, other := range m.batches.all {
		if other.Datasource != b.Datasource {
			continue
		}
		if b.RequestKey != "" && other.RequestKey == b.RequestKey {
			return cloneBatch(other), false, nil
		}
		if other.FinishedAt.IsZero() {
			open++
		}
	}
	if open >= maxOpen {
		return Batch{}, false, ErrTooManyBatches
	}
	if m.batches.all == nil {
		m.batches.all = map[string]Batch{}
	}
	m.batches.all[b.ID] = cloneBatch(b)
	return b, true, nil
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
	return out[:min(max(limit, 0), len(out))], nil
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

// owned is the unfinished batch id while owner owns it.
func (m *MemoryStore) owned(id, owner string) (Batch, error) {
	b, ok := m.batches.all[id]
	if !ok || b.Owner != owner || !b.FinishedAt.IsZero() {
		return Batch{}, ErrNotOwner
	}
	return b, nil
}

func (m *MemoryStore) MoveItem(
	_ context.Context, id, owner string, now time.Time, item BatchItem, from ...string,
) (bool, error) {
	m.batches.mu.Lock()
	defer m.batches.mu.Unlock()
	b, err := m.owned(id, owner)
	if err != nil {
		return false, err
	}
	b.Heartbeat = now
	i := slices.IndexFunc(b.Items, func(it BatchItem) bool { return it.Schema == item.Schema })
	if i < 0 || !slices.Contains(from, b.Items[i].State) {
		m.batches.all[id] = b
		return false, nil
	}
	b.Items = slices.Clone(b.Items)
	b.Items[i] = item
	m.batches.all[id] = b
	return true, nil
}

func (m *MemoryStore) FinishBatch(_ context.Context, id, owner string, at time.Time) error {
	m.batches.mu.Lock()
	defer m.batches.mu.Unlock()
	b, err := m.owned(id, owner)
	if err != nil {
		return err
	}
	b.Items = slices.Clone(b.Items)
	for i := range b.Items {
		if st := b.Items[i].State; st == ItemPending || st == ItemRunning {
			b.Items[i].State = ItemCanceled
		}
	}
	b.FinishedAt, b.Heartbeat = at, at
	m.batches.all[id] = b
	return nil
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
