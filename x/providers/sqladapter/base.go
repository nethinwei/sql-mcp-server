package sqladapter

import (
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
)

// Base is what the providers on database/sql share: the pool and the
// dialect, EXPLAIN estimator and schema introspector of one database. A
// provider embeds it and adds what is its own.
type Base struct {
	*Pool
	dialect      dialect.Dialect
	explainer    cost.Explainer
	introspector introspect.Introspector
	limits       dialect.Profile
}

// NewBase is a Base on pool with the given adapters.
func NewBase(pool *Pool, d dialect.Dialect, e cost.Explainer, i introspect.Introspector) Base {
	return Base{Pool: pool, dialect: d, explainer: e, introspector: i}
}

// WithLimits returns b with the claims that depend on how its connection was
// opened (timeouts, native write refusals).
func (b Base) WithLimits(limits dialect.Profile) Base {
	b.limits = limits
	return b
}

// Profile is the connection's capabilities: its dialect's and its limits.
func (b Base) Profile() dialect.Profile {
	var caps dialect.Profile
	if b.dialect != nil {
		caps = b.dialect.Capabilities()
	}
	return caps.With(b.limits)
}

// Dialect returns the database's dialect.
func (b Base) Dialect() dialect.Dialect { return b.dialect }

// Explainer returns the EXPLAIN-based plan estimator.
func (b Base) Explainer() cost.Explainer { return b.explainer }

// Introspector returns the schema introspector.
func (b Base) Introspector() introspect.Introspector { return b.introspector }
