package bootstrap

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"
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
// handlers. A successful reload publishes the new App at once, so requests
// never wait for a reload, and drains and closes the old one in the
// background; a failed build leaves the current App untouched.
type Runtime struct {
	current  atomic.Pointer[appSnapshot]
	reload   sync.Mutex
	retiring sync.WaitGroup
	build    func(string) (*App, error)
	revoked  atomic.Pointer[func([]string)]
	stale    atomic.Pointer[StaleState]
	applied  atomic.Int64
}

// OnRevokedPrincipals registers fn to receive the principal keys of users that
// a successful reload removed or disabled, after the new snapshot is published.
// Transports use it to drop those users' sessions and roll back their
// transactions.
func (r *Runtime) OnRevokedPrincipals(fn func([]string)) {
	r.revoked.Store(&fn)
}

// UserByTokenHash resolves a tokenHash against the current snapshot.
func (r *Runtime) UserByTokenHash(hash string) (UserIdentity, bool) {
	app := r.Current()
	if app == nil {
		return UserIdentity{}, false
	}
	return app.UserByTokenHash(hash)
}

// UserByName resolves an enabled user against the current snapshot.
func (r *Runtime) UserByName(name string) (UserIdentity, bool) {
	app := r.Current()
	if app == nil {
		return UserIdentity{}, false
	}
	return app.UserByName(name)
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
	return r.reloadWith(func() (*App, error) { return r.build(path) })
}

func (r *Runtime) reloadWith(build func() (*App, error)) error {
	r.reload.Lock()
	defer r.reload.Unlock()
	if r.current.Load() == nil {
		return ErrRuntimeClosed
	}
	next, err := build()
	if err != nil {
		return err
	}
	old := r.current.Load()
	if err := preserveReloadTransactions(old.app, next); err != nil {
		return err
	}
	preserveReloadBudget(old.app, next)
	if old.app.Writes != nil {
		next.Writes = old.app.Writes // sessions keep reading their own writes across a reload
	}
	publishReload(r, old, next, revokedPrincipals(old.app, next))
	return nil
}

// revokedPrincipals lists users servable by old but no longer by next.
func revokedPrincipals(old, next *App) []string {
	var out []string
	for name, id := range old.Users {
		if _, ok := next.Users[name]; !ok {
			out = append(out, id.Principal)
		}
	}
	return out
}

func preserveReloadTransactions(old, next *App) error {
	if old.Transactions == nil {
		return nil
	}
	if next.Transactions == nil {
		_ = next.Close()
		return errors.New("bootstrap: transaction configuration cannot be removed by reload")
	}
	oldTTL, oldMax := old.Transactions.Configuration()
	nextTTL, nextMax := next.Transactions.Configuration()
	if oldTTL != nextTTL || oldMax != nextMax {
		_ = next.Close()
		return fmt.Errorf(
			"bootstrap: transaction ttl/maxOpen change requires restart (current %s/%d, requested %s/%d)",
			oldTTL,
			oldMax,
			nextTTL,
			nextMax,
		)
	}
	if next.Transactions != old.Transactions {
		next.Transactions.Close()
	}
	next.Transactions = old.Transactions
	return nil
}

// preserveReloadBudget keeps the accumulated session state across a reload,
// adopting the new configured limits.
func preserveReloadBudget(old, next *App) {
	if old.Budget == nil || next.Budget == nil {
		return
	}
	old.Budget.UpdateLimits(next.Budget.ConfiguredLimits())
	next.Budget = old.Budget
}

// publishReload serves next at once and retires old in the background: once
// the requests that leased old finish, the users the reload revoked are
// reported (so transactions those requests opened are rolled back too) and old
// is closed.
func publishReload(r *Runtime, old *appSnapshot, next *App, revoked []string) {
	shared := next.Transactions == old.app.Transactions // the next App carries them on
	r.current.Store(newAppSnapshot(next))
	old.mu.Lock()
	old.retired = true
	old.cond.Broadcast()
	old.mu.Unlock()
	r.retiring.Go(func() {
		old.mu.Lock()
		for old.refs > 0 {
			old.cond.Wait()
		}
		if shared {
			old.app.Transactions = nil
		}
		old.mu.Unlock()
		if fn := r.revoked.Load(); fn != nil && len(revoked) > 0 {
			(*fn)(revoked)
		}
		if err := old.app.Close(); err != nil {
			slog.Warn("closing the replaced snapshot failed", "error", err.Error())
		}
	})
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

// SnapshotReady reports whether a configuration snapshot is currently
// published and servable. It backs the /readyz/snapshot readiness probe.
func (r *Runtime) SnapshotReady(context.Context) error {
	if r.Current() == nil {
		return ErrRuntimeClosed
	}
	return nil
}

// DatabasesReady pings every database of the current snapshot. It backs the
// /readyz/db readiness probe.
func (r *Runtime) DatabasesReady(ctx context.Context) error {
	app, release, err := r.Acquire()
	if err != nil {
		return err
	}
	defer release()
	return app.Ping(ctx)
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
	if app.Budget != nil {
		app.Budget.CloseSession(session)
	}
	app.Writes.Forget(session)
}

// Close stops new acquisitions, drains leases, and closes the current App and
// the ones reloads replaced.
func (r *Runtime) Close() error {
	r.reload.Lock()
	defer r.reload.Unlock()
	defer r.retiring.Wait()
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
