package providerregistry

import (
	"errors"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/config"
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

// The native timeout leaves the request's deadline a margin: a tenth, at
// most a second; down to the shortest timeout configuration accepts, it stays
// shorter than the request's and whole milliseconds, and it never drops to
// nothing.
func TestNativeTimeout(t *testing.T) {
	for timeout, want := range map[time.Duration]time.Duration{
		30 * time.Second:       29 * time.Second,
		5 * time.Second:        4500 * time.Millisecond,
		300 * time.Millisecond: 270 * time.Millisecond,
		config.MinQueryTimeout: 90 * time.Millisecond,
		time.Millisecond:       time.Millisecond,
	} {
		if got := (Options{Timeout: timeout}).NativeTimeout(); got != want {
			t.Errorf("NativeTimeout(%v) = %v, want %v", timeout, got, want)
		}
	}
}
