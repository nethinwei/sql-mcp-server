// Package provider defines the database adapter contract consumed by the
// application assembly layer.
package provider

import (
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/core/store"
)

// Provider aggregates the core interfaces a database adapter must satisfy.
type Provider interface {
	store.DB
	store.TxBeginner
	Dialect() dialect.Dialect
	// Profile is what this connection guarantees: its dialect's capabilities
	// and the limits that depend on how it was opened.
	Profile() dialect.Profile
	Explainer() cost.Explainer
	Introspector() introspect.Introspector
	Close() error
}
