// Package version exposes build metadata shared by every server surface.
package version

// value is overridden for release builds with:
//
//	go build -ldflags "-X github.com/nethinwei/sql-mcp-server/version.value=v0.1.1"
var value = "dev"

// String returns the version injected at build time.
func String() string {
	return value
}
