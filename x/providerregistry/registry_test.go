package providerregistry

import (
	"errors"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/provider"
)

func TestRegisterAndLookup(t *testing.T) {
	const name = "registry-test"
	Register(name, func(string, Options) (provider.Provider, error) {
		return nil, nil
	})
	if !IsRegistered(name) {
		t.Fatalf("IsRegistered(%q) = false", name)
	}
	if _, err := New(name, "", Options{Timeout: time.Second}); err != nil {
		t.Fatalf("New(%q): %v", name, err)
	}
}

func TestUnknownDriver(t *testing.T) {
	if _, err := New("missing-registry-test", "", Options{Timeout: time.Second}); !errors.Is(err, ErrUnsupportedDriver) {
		t.Fatalf("New() error = %v, want ErrUnsupportedDriver", err)
	}
}
