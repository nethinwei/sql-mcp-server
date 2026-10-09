package bootstrap

import (
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// ErrRuntimeClosed is returned when a closed runtime is used.
var ErrRuntimeClosed = errors.New("bootstrap: runtime closed")

// ErrReloadBacklog is returned by a reload while maxRetiring replaced
// snapshots still serve requests: each holds its own engine and prepared
// statements, so reloads wait for them to drain rather than pile them up.
var ErrReloadBacklog = errors.New("bootstrap: earlier configurations are still draining; try again")

const maxRetiring = 3

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
	current   atomic.Pointer[appSnapshot]
	reload    sync.Mutex
	retiring  sync.WaitGroup
	draining  atomic.Int32 // replaced snapshots not closed yet
	build     func(string) (*App, error)
	revoked   atomic.Pointer[func([]string) []string]
	onPublish struct {
		sync.Mutex
		fns []func(*App)
	}
	stale   atomic.Pointer[StaleState]
	applied atomic.Int64
}

// OnRevokedPrincipals registers fn to receive the principal keys of users that
// a successful reload removed or disabled, as the new snapshot is published.
// Transports use it to drop those users' sessions and roll back their
// transactions; fn returns the sessions it dropped. Once requests on the old
// snapshot finish, transactions they opened in those sessions are rolled back
// too. Sessions are named by their IDs, never reused, so a user enabled again
// keeps the sessions it opens meanwhile.
func (r *Runtime) OnRevokedPrincipals(fn func([]string) []string) {
	r.revoked.Store(&fn)
}

// OnPublish calls fn with the current App and then with every App a reload
// publishes, as it is published. Registering and the first call happen
// between reloads, so fn misses none. Transports use it to follow the
// configuration: the tools they list, how they authenticate.
func (r *Runtime) OnPublish(fn func(*App)) {
	r.reload.Lock()
	defer r.reload.Unlock()
	r.onPublish.Lock()
	r.onPublish.fns = append(r.onPublish.fns, fn)
	r.onPublish.Unlock()
	if current := r.Current(); current != nil {
		fn(current)
	}
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
	app.publishShared()
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
	if r.draining.Load() >= maxRetiring {
		return ErrReloadBacklog
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
	next.publishShared()
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
	// Open transactions carry on under the new limits.
	old.Transactions.UpdateLimits(next.Transactions.Configuration())
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

// publishReload serves next at once, drops the sessions of the users the
// reload revoked, and retires old in the background: once the requests that
// leased old finish, transactions they opened in those sessions are rolled
// back and old is closed.
func publishReload(r *Runtime, old *appSnapshot, next *App, revoked []string) {
	shared := next.Transactions == old.app.Transactions // the next App carries them on
	r.current.Store(newAppSnapshot(next))
	old.mu.Lock()
	old.retired = true
	old.cond.Broadcast()
	old.mu.Unlock()
	r.onPublish.Lock()
	for _, fn := range r.onPublish.fns {
		fn(next)
	}
	r.onPublish.Unlock()
	var sessions []string
	if fn := r.revoked.Load(); fn != nil && len(revoked) > 0 {
		sessions = (*fn)(revoked)
	}
	r.draining.Add(1)
	r.retiring.Go(func() {
		defer r.draining.Add(-1)
		old.mu.Lock()
		for old.refs > 0 {
			old.cond.Wait()
		}
		if shared {
			old.app.Transactions = nil
		}
		old.mu.Unlock()
		for _, session := range sessions {
			r.RollbackSession(session)
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
