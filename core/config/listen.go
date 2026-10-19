package config

import (
	"fmt"
	"net"
)

// MinSharedTokenLength is the shortest server.auth.token accepted on a
// listener beyond loopback: the shared token is the only credential of every
// caller that presents it, so it must resist guessing (32 bytes, such as
// the output of openssl rand -hex 16, or longer).
const MinSharedTokenLength = 32

// IsLoopbackAddr reports whether a listen address binds only the loopback
// interface. An empty host (e.g. ":8080") or a wildcard binds all interfaces
// and is treated as non-loopback (exposed).
func IsLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		return false
	}
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// CheckSharedToken refuses a shared token shorter than MinSharedTokenLength
// on a listener beyond loopback; on loopback any token is accepted.
func CheckSharedToken(addr, token string) error {
	if token == "" || len(token) >= MinSharedTokenLength || IsLoopbackAddr(addr) {
		return nil
	}
	return fmt.Errorf("server.auth.token has %d bytes; a listener beyond loopback (%q) needs at least %d "+
		"(generate one with: openssl rand -hex 32)", len(token), addr, MinSharedTokenLength)
}
