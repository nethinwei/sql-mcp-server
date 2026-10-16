package bootstrap

import (
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/config"
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
	current atomic.Pointer[appSnapshot]
	// reload serializes reloads, builds included; publish guards what follows
	// a build (carrying state over from the current App and publishing), which
	// CloseContext waits for, as it never waits for a build.
	reload    sync.Mutex
	publish   sync.Mutex
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
	// held are the users a published revision not applied yet changes (see
	// holdChanged); a reload that publishes releases them.
	held atomic.Pointer[map[string]bool]
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

// UserByTokenHash resolves a tokenHash against the current snapshot; a held
// user is not found.
func (r *Runtime) UserByTokenHash(hash string) (UserIdentity, bool) {
	app := r.Current()
	if app == nil {
		return UserIdentity{}, false
	}
	name, ok := app.UserTokens[hash]
	if !ok {
		return UserIdentity{}, false
	}
	return r.UserByName(name)
}

// UserByName resolves an enabled user against the current snapshot; a held
// user is not found.
func (r *Runtime) UserByName(name string) (UserIdentity, bool) {
	app := r.Current()
	if app == nil || r.heldUser(name) {
		return UserIdentity{}, false
	}
	return app.UserByName(name)
}

// NewRuntimeWithBuilder creates a runtime with an injected reload builder.
// It is useful for embedders and tests that assemble providers themselves.
// Process state (transactions, budgets, write tracking) outlives a reload
// only when every App is assembled on one Shared; an App assembled without
// it owns that state, which goes as the App is retired (see
// docs/design/lifetimes.md).
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
	r.publish.Lock()
	defer r.publish.Unlock()
	old := r.current.Load()
	if old == nil { // closed while building (Close does not wait for a build)
		_ = next.Close()
		return ErrRuntimeClosed
	}
	next.publishShared()
	if err := publishReload(r, old, next, revokedPrincipals(old.app, next)); err != nil {
		return err
	}
	r.held.Store(nil)
	return nil
}

// revokedPrincipals lists users servable by old but no longer by next, and
// those whose subject next changes: an MCP session is bound to the subject
// that opened it, so theirs must end for the client to open one under the
// new subject (it gets "session not found", not a refusal).
func revokedPrincipals(old, next *App) []string {
	var out []string
	for name, id := range old.Users {
		if now, ok := next.Users[name]; !ok || !reflect.DeepEqual(now.Subject, id.Subject) {
			out = append(out, id.Principal)
		}
	}
	return out
}

// publishReload serves next at once, drops the sessions of the users the
// reload revoked, and retires old in the background: once the requests that
// leased old finish, transactions they opened in those sessions are rolled
// back and old is closed.
func publishReload(r *Runtime, old *appSnapshot, next *App, revoked []string) error {
	if !r.current.CompareAndSwap(old, newAppSnapshot(next)) {
		_ = next.Close() // closed meanwhile
		return ErrRuntimeClosed
	}
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
	next.Bus().Fork(&r.retiring, "retire snapshot", func(context.Context) {
		defer r.draining.Add(-1)
		old.mu.Lock()
		for old.refs > 0 {
			old.cond.Wait()
		}
		old.mu.Unlock()
		for _, session := range sessions {
			r.RollbackSession(session)
		}
		if err := old.app.Close(); err != nil {
			slog.Warn("closing the replaced snapshot failed", "error", err.Error())
		}
	})
	return nil
}

// Watch polls path for content changes and reloads it. Reload errors are
// reported through onError and do not stop the watcher or replace the old App.
func (r *Runtime) Watch(ctx context.Context, path string, interval time.Duration, onError func(error)) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	last, report := sha256.Sum256(data), reporter(onError)
	return poll(ctx, interval, time.Second, func() {
		data, err := os.ReadFile(path)
		if err != nil {
			report(err)
			return
		}
		sum := sha256.Sum256(data)
		if sum == last {
			return
		}
		if err := r.Reload(path); err != nil {
			report(err)
			r.holdLoaded(LoadBytes(data))
			return
		}
		last = sum
	})
}

// poll runs tick every interval (def when interval is not positive) until
// ctx ends, and returns why it ended.
func poll(ctx context.Context, interval, def time.Duration, tick func()) error {
	if interval <= 0 {
		interval = def
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			tick()
		}
	}
}

// reporter reports errors to onError, or nowhere.
func reporter(onError func(error)) func(error) {
	if onError == nil {
		return func(error) {}
	}
	return onError
}

// holdLoaded holds the users a configuration that failed to apply changes,
// when it loads (see holdChanged).
func (r *Runtime) holdLoaded(cfg *config.Config, err error) {
	if err == nil {
		r.holdChanged(cfg)
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

// Close is CloseContext without a deadline.
func (r *Runtime) Close() error {
	return r.CloseContext(context.Background())
}

// CloseContext stops new acquisitions at once, drains leases, and closes the
// current App and the ones reloads replaced. It does not wait for a reload
// being built: that reload closes what it built. Past ctx's deadline it
// returns ctx's error and leaves the rest to finish in the background (a
// stuck database call or audit sink must not hold up a process exit).
func (r *Runtime) CloseContext(ctx context.Context) error {
	old := r.current.Swap(nil)
	if old == nil {
		return nil
	}
	old.mu.Lock()
	old.retired = true
	old.cond.Broadcast()
	old.mu.Unlock()
	done := make(chan error, 1)
	var closing sync.WaitGroup
	old.app.Bus().Fork(&closing, "close runtime", func(context.Context) {
		// A reload past its build carries on from old and publishes: let it
		// finish (it then finds the runtime closed) before closing old.
		r.publish.Lock()
		r.publish.Unlock() //nolint:staticcheck // a barrier
		old.mu.Lock()
		for old.refs > 0 {
			old.cond.Wait()
		}
		old.mu.Unlock()
		err := old.app.CloseContext(ctx)
		r.retiring.Wait()
		if old.app.shared != nil { // the last App: the process's transactions end
			old.app.shared.Close()
		}
		done <- err
	})
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
