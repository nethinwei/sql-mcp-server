// Package pgimage names the PostgreSQL image the provider's integration tests
// run on, so that CI runs them on every supported version
// (docs/supported-versions.md) from one setting.
package pgimage

import "os"

// Default is the image used when SQL_MCP_PG_IMAGE is unset.
const Default = "postgres:16-alpine"

// Image is SQL_MCP_PG_IMAGE, or Default.
func Image() string {
	if image := os.Getenv("SQL_MCP_PG_IMAGE"); image != "" {
		return image
	}
	return Default
}
