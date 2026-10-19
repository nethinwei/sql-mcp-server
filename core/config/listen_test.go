package config

import (
	"strings"
	"testing"
)

func TestIsLoopbackAddr(t *testing.T) {
	t.Parallel()
	for addr, want := range map[string]bool{
		"127.0.0.1:8080":    true,
		"localhost:8080":    true,
		"[::1]:8080":        true,
		":8080":             false,
		"0.0.0.0:8080":      false,
		"[::]:8080":         false,
		"192.168.1.10:8080": false,
	} {
		if got := IsLoopbackAddr(addr); got != want {
			t.Errorf("IsLoopbackAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestSharedTokenLength(t *testing.T) {
	t.Parallel()
	strong := strings.Repeat("a", MinSharedTokenLength)
	for _, c := range []struct {
		addr, token string
		ok          bool
	}{
		{"0.0.0.0:8080", "x", false},
		{"0.0.0.0:8080", strong[1:], false},
		{"0.0.0.0:8080", strong, true},
		{"127.0.0.1:8080", "x", true},
		{"0.0.0.0:8080", "", true},
	} {
		cfg := &Config{Server: ServerConfig{Addr: c.addr, Auth: AuthConfig{Token: c.token}},
			Database: DatabaseConfig{Driver: "postgres", DSN: "x"}}
		cfg.ApplyDefaults()
		if err := cfg.Validate(); (err == nil) != c.ok {
			t.Errorf("addr %q token of %d bytes: Validate = %v, want ok %v", c.addr, len(c.token), err, c.ok)
		}
	}
}
