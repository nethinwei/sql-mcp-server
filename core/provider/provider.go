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
	Explainer() cost.Explainer
	Introspector() introspect.Introspector
	Close() error
}
