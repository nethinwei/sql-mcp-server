// Package providerregistry maps database driver names to provider factories.
package providerregistry

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/provider"
)

// ErrUnsupportedDriver is returned when no factory is registered for a driver.
var ErrUnsupportedDriver = errors.New("providerregistry: unsupported driver")

// Factory opens a database provider.
type Factory func(dsn string, timeout time.Duration) (provider.Provider, error)

var (
	mu        sync.RWMutex
	factories = make(map[string]Factory)
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

// New opens the provider registered under driver.
func New(driver, dsn string, timeout time.Duration) (provider.Provider, error) {
	mu.RLock()
	factory, ok := factories[driver]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedDriver, driver)
	}
	return factory(dsn, timeout)
}

// IsRegistered reports whether driver has a registered factory.
func IsRegistered(driver string) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := factories[driver]
	return ok
}

// KnownDrivers returns registered names in deterministic order.
func KnownDrivers() []string {
	mu.RLock()
	names := make([]string, 0, len(factories))
	for name := range factories {
		names = append(names, name)
	}
	mu.RUnlock()
	sort.Strings(names)
	return names
}
