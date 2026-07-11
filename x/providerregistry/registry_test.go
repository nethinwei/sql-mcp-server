package providerregistry

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/provider"
)

func TestRegisterAndLookup(t *testing.T) {
	const name = "registry-test"
	Register(name, func(string, time.Duration) (provider.Provider, error) {
		return nil, nil
	})
	if !IsRegistered(name) {
		t.Fatalf("IsRegistered(%q) = false", name)
	}
	if !slices.Contains(KnownDrivers(), name) {
		t.Fatalf("KnownDrivers() does not contain %q", name)
	}
	if _, err := New(name, "", time.Second); err != nil {
		t.Fatalf("New(%q): %v", name, err)
	}
}

func TestUnknownDriver(t *testing.T) {
	if _, err := New("missing-registry-test", "", time.Second); !errors.Is(err, ErrUnsupportedDriver) {
		t.Fatalf("New() error = %v, want ErrUnsupportedDriver", err)
	}
}
