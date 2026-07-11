package bootstrap

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/budget"
)

// ErrRuntimeClosed is returned when a closed runtime is used.
var ErrRuntimeClosed = errors.New("bootstrap: runtime closed")

type appSnapshot struct {
	app     *App
	mu      sync.Mutex
	cond    *sync.Cond
	refs    int
	retired bool
}

func newAppSnapshot(app *App) *appSnapshot {
	s := &appSnapshot{app: app}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// Runtime owns the atomically replaceable App snapshot used by serving
// handlers. A successful reload publishes the new App before draining and
// closing the old one; a failed build leaves the current App untouched.
type Runtime struct {
	current atomic.Pointer[appSnapshot]
	reload  sync.Mutex
	build   func(string) (*App, error)
}

// NewRuntime creates a runtime using Load followed by Assemble for reloads.
func NewRuntime(app *App) *Runtime {
	return NewRuntimeWithBuilder(app, func(path string) (*App, error) {
		cfg, err := Load(path)
		if err != nil {
			return nil, err
		}
		return Assemble(cfg)
	})
}

// NewRuntimeWithBuilder creates a runtime with an injected reload builder.
// It is useful for embedders and tests that assemble providers themselves.
func NewRuntimeWithBuilder(app *App, build func(string) (*App, error)) *Runtime {
	r := &Runtime{build: build}
	r.current.Store(newAppSnapshot(app))
	return r
}

// Current returns the currently published App, or nil after Close. Handlers
// that execute work must use Acquire so the App cannot close during the call.
func (r *Runtime) Current() *App {
	if snapshot := r.current.Load(); snapshot != nil {
		return snapshot.app
	}
	return nil
}

// Acquire leases the current snapshot until the returned release function is
// called. It retries when racing with retirement.
func (r *Runtime) Acquire() (*App, func(), error) {
	for {
		snapshot := r.current.Load()
		if snapshot == nil {
			return nil, nil, ErrRuntimeClosed
		}
		snapshot.mu.Lock()
		if snapshot.retired {
			for snapshot.retired && r.current.Load() == snapshot {
				snapshot.cond.Wait()
			}
			snapshot.mu.Unlock()
			continue
		}
		snapshot.refs++
		snapshot.mu.Unlock()
		return snapshot.app, func() {
			snapshot.mu.Lock()
			snapshot.refs--
			if snapshot.refs == 0 {
				snapshot.cond.Broadcast()
			}
			snapshot.mu.Unlock()
		}, nil
	}
}

// Reload performs parse/default/validate/secret-resolution/assembly before
// atomically publishing the new App. Build failure preserves the old snapshot.
func (r *Runtime) Reload(path string) error {
	r.reload.Lock()
	defer r.reload.Unlock()
	if r.current.Load() == nil {
		return ErrRuntimeClosed
	}
	next, err := r.build(path)
	if err != nil {
		return err
	}
	old := r.current.Load()
	oldBudget, oldBudgetOK := old.app.Budget.(*budget.MemoryManager)
	nextBudget, nextBudgetOK := next.Budget.(*budget.MemoryManager)
	if old.app.Budget != nil && (!oldBudgetOK || !nextBudgetOK) && next.Budget != old.app.Budget {
		_ = next.Close()
		return errors.New("bootstrap: budget manager does not support state-preserving reload")
	}
	if old.app.Transactions != nil {
		if next.Transactions == nil {
			_ = next.Close()
			return errors.New("bootstrap: transaction configuration cannot be removed by reload")
		}
		oldTTL, oldMax := old.app.Transactions.Configuration()
		nextTTL, nextMax := next.Transactions.Configuration()
		if oldTTL != nextTTL || oldMax != nextMax {
			_ = next.Close()
			return fmt.Errorf("bootstrap: transaction ttl/maxOpen change requires restart (current %s/%d, requested %s/%d)", oldTTL, oldMax, nextTTL, nextMax)
		}
		if next.Transactions != old.app.Transactions {
			next.Transactions.Close()
		}
		next.Transactions = old.app.Transactions
	}
	if old.app.Budget != nil {
		if oldBudgetOK && nextBudgetOK {
			roles, tenants := nextBudget.ConfiguredLimits()
			oldBudget.UpdateLimits(roles, tenants)
			next.Budget = oldBudget
		}
	}
	old.mu.Lock()
	old.retired = true
	for old.refs > 0 {
		old.cond.Wait()
	}
	r.current.Store(newAppSnapshot(next))
	old.cond.Broadcast()
	if next.Transactions == old.app.Transactions {
		old.app.Transactions = nil
	}
	old.mu.Unlock()
	return old.app.Close()
}

// Watch polls path for content changes and reloads it. Reload errors are
// reported through onError and do not stop the watcher or replace the old App.
func (r *Runtime) Watch(ctx context.Context, path string, interval time.Duration, onError func(error)) error {
	if interval <= 0 {
		interval = time.Second
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	last := sha256.Sum256(data)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			data, err := os.ReadFile(path)
			if err != nil {
				if onError != nil {
					onError(err)
				}
				continue
			}
			sum := sha256.Sum256(data)
			if sum == last {
				continue
			}
			if err := r.Reload(path); err != nil {
				if onError != nil {
					onError(err)
				}
				continue
			}
			last = sum
		}
	}
}

// RollbackSession rolls back transactions associated with a disconnected MCP
// session when the transport exposes a stable non-empty session ID.
func (r *Runtime) RollbackSession(session string) {
	app, release, err := r.Acquire()
	if err != nil {
		return
	}
	defer release()
	if app.Transactions != nil {
		app.Transactions.RollbackSession(session)
	}
	if manager, ok := app.Budget.(budget.SessionManager); ok {
		manager.CloseSession(session)
	}
}

// Close stops new acquisitions, drains leases, and closes the current App.
func (r *Runtime) Close() error {
	r.reload.Lock()
	defer r.reload.Unlock()
	old := r.current.Swap(nil)
	if old == nil {
		return nil
	}
	old.mu.Lock()
	old.retired = true
	old.cond.Broadcast()
	for old.refs > 0 {
		old.cond.Wait()
	}
	old.mu.Unlock()
	return old.app.Close()
}
