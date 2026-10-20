// Package providerregistry maps database driver names to provider factories.
package providerregistry

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/core/provider"
	"github.com/nethinwei/sql-mcp-server/core/reason"
)

// ErrUnsupportedDriver is returned when no factory is registered for a driver.
var ErrUnsupportedDriver = errors.New("providerregistry: unsupported driver")

// Options configures an opened provider.
type Options struct {
	// Timeout is the statement timeout the provider sets natively; 0 sets
	// none (see WithoutTimeout).
	Timeout time.Duration
	// Pooler is "transaction" when the connection goes through a
	// transaction-mode pooler, which keeps no session state: the provider
	// must not rely on named prepared statements or startup settings.
	Pooler string
	// Context bounds opening the provider (its first connection); nil means
	// no bound.
	Context context.Context
	// Lazy opens the provider without reaching the database, which its first
	// use reaches: a database down when the provider opens does not fail it.
	Lazy bool
}

// OpenContext is the context opening the provider runs under.
func (o Options) OpenContext() context.Context {
	if o.Context != nil {
		return o.Context
	}
	return context.Background()
}

// NativeTimeout is the statement timeout a provider sets on the database:
// Timeout less a margin (a tenth, at most a second), so the database stops a
// slow statement, and reports it stopped, before the request's own deadline
// cuts the connection and leaves a write's outcome unknown. The request's
// deadline starts before planning, so this makes the database usually, not
// always, the first to stop. Configuration keeps Timeout at
// config.MinQueryTimeout or more, which leaves the margin whole milliseconds;
// a shorter Timeout given in code still gets at least a millisecond, as 0 is
// no timeout at all on PostgreSQL.
func (o Options) NativeTimeout() time.Duration {
	return max(o.Timeout-min(o.Timeout/10, time.Second), time.Millisecond)
}

// NoTimeout is the claim on the statement timeouts of a connection opened
// without one (Timeout 0, cost.queryTimeout 0): none is set, and nothing
// stops a statement at a deadline but the database's own defaults.
var NoTimeout = dialect.Claim{Strength: dialect.Unsupported, Evidence: dialect.Declared,
	Reason: reason.New(reason.NoQueryTimeout)}

// WithoutTimeout is p for a connection opened with opts: without a timeout
// (Timeout 0), its timeout claims are NoTimeout.
func (o Options) WithoutTimeout(p dialect.Profile) dialect.Profile {
	if o.Timeout > 0 {
		return p
	}
	p[dialect.TimeoutRead], p[dialect.TimeoutWrite] = NoTimeout, NoTimeout
	return p
}

// PoolerTransaction is the transaction-mode pooler setting.
const PoolerTransaction = "transaction"

// Factory opens a database provider.
type Factory func(dsn string, opts Options) (provider.Provider, error)

// LimitsFunc judges, without connecting, what a connection to dsn opened
// with opts guarantees on the server (its statement timeouts and native
// write refusals).
type LimitsFunc func(dsn string, opts Options) (dialect.Profile, error)

// ErrNoLimits is returned for a driver that registered no LimitsFunc.
var ErrNoLimits = errors.New("providerregistry: the driver declares no limits")

var (
	mu        sync.RWMutex
	factories = make(map[string]Factory)
	limits    = make(map[string]LimitsFunc)
)

// Register adds a named provider factory. It panics for invalid or duplicate
// registrations so built-in driver mistakes fail during process startup.
func Register(name string, factory Factory) {
	if name == "" {
		panic("providerregistry: empty driver name")
	}
	if factory == nil {
		panic("providerregistry: nil factory for " + name)
	}
	mu.Lock()
	defer mu.Unlock()
	if _, exists := factories[name]; exists {
		panic("providerregistry: duplicate driver " + name)
	}
	factories[name] = factory
}

// RegisterLimits adds the LimitsFunc of a driver. Like Register, it panics
// for invalid or duplicate registrations.
func RegisterLimits(name string, fn LimitsFunc) {
	if name == "" || fn == nil {
		panic("providerregistry: invalid limits registration for " + name)
	}
	mu.Lock()
	defer mu.Unlock()
	if _, exists := limits[name]; exists {
		panic("providerregistry: duplicate limits for " + name)
	}
	limits[name] = fn
}

// Limits judges a connection of driver to dsn opened with opts, without
// connecting (see LimitsFunc).
func Limits(driver, dsn string, opts Options) (dialect.Profile, error) {
	mu.RLock()
	fn, ok := limits[driver]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrNoLimits, driver)
	}
	return fn(dsn, opts)
}

// HasLimits reports whether driver registered a LimitsFunc.
func HasLimits(driver string) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := limits[driver]
	return ok
}

// New opens the provider registered under driver.
func New(driver, dsn string, opts Options) (provider.Provider, error) {
	mu.RLock()
	factory, ok := factories[driver]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedDriver, driver)
	}
	return factory(dsn, opts)
}

// Drivers lists the registered driver names, sorted.
func Drivers() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(factories))
	for name := range factories {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// IsRegistered reports whether driver has a registered factory.
func IsRegistered(driver string) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := factories[driver]
	return ok
}
