package dialect

import (
	"errors"
	"fmt"
)

// ErrUnknownDialect is returned by New for an unrecognized dialect name.
var ErrUnknownDialect = errors.New("unknown dialect")

// Capabilities declares what a database supports. It drives codegen rendering
// (e.g. RETURNING fallback) and which Gate layers are assembled (e.g. whether
// the Estimate layer is enabled, which DB-native limits apply). See the cost
// gate design.
type Capabilities struct {
	// Rendering.
	Returning    bool // INSERT ... RETURNING (PG/OB-Oracle have; MySQL lacks -> second SELECT for last id)
	Savepoint    bool
	KeysetCursor bool // keyset pagination needs no degradation

	// Gate layers (see cost.Gate assembly).
	ExplainJSON      bool // supports EXPLAIN ... JSON output
	ExplainCost      bool // EXPLAIN yields a numeric cost (SQLite does not)
	ExplainAccurate  bool // estimate trustworthiness (PG high; MySQL/OB medium; SQLite low)
	StatementTimeout bool // native timeout exists; core does not SET it across pooled connections
	ScanRowCap       bool // ob max_read_size / mysql max_join_size (runtime scan-row hard cap)
	SQLSafeUpdates   bool // mysql/ob sql_safe_updates (prevents full-table writes)
	ResourceManager  bool // ob tenant / oracle resource isolation
}

// Dialect abstracts the SQL differences core codegen needs. Implementations are
// stateless and safe for concurrent use.
type Dialect interface {
	Name() string
	QuoteIdent(name string) string
	Placeholder(index int) string
	ExplainSQL(query string) string
	Capabilities() Capabilities
}

// New returns the Dialect for the given name, or an error wrapping
// ErrUnknownDialect.
func New(name string) (Dialect, error) {
	switch name {
	case "postgres":
		return Postgres{}, nil
	case "mysql":
		return MySQL{}, nil
	case "oceanbase":
		return OceanBase{}, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownDialect, name)
	}
}
