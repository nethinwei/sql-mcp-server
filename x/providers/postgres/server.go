package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// The servers this provider is built for are PostgreSQL itself, 11 (whose
// pg_index has indnkeyatts) to 18. A server that speaks its protocol but is
// of another kind differs in its catalogs, plans, transactions or RETURNING,
// so it is refused when the provider first reaches it rather than served half
// right; docs/supported-versions.md says the same.
const (
	minServerVersion = 110000
	maxServerVersion = 190000 // exclusive
)

// ErrUnsupportedServer is a server outside the versions and kind supported.
var ErrUnsupportedServer = errors.New("postgres: unsupported server")

// derivatives are the markers in version() of servers that speak the
// PostgreSQL protocol but are not PostgreSQL.
var derivatives = []string{"hologres", "greenplum", "redshift", "cockroachdb", "opengauss", "gaussdb", "yugabyte"}

// checkServer tells whether the server whose server_version_num is num and
// version() is version is supported; hologres is whether it has Hologres's
// hg_version() function.
func checkServer(num int, version string, hologres bool) error {
	lower := strings.ToLower(version)
	for _, d := range derivatives {
		if strings.Contains(lower, d) {
			return fmt.Errorf("%w: %q is not PostgreSQL; only PostgreSQL 11 to 18 is supported",
				ErrUnsupportedServer, version)
		}
	}
	if hologres || !strings.HasPrefix(version, "PostgreSQL ") {
		return fmt.Errorf("%w: %q is not PostgreSQL; only PostgreSQL 11 to 18 is supported",
			ErrUnsupportedServer, version)
	}
	if num < minServerVersion || num >= maxServerVersion {
		return fmt.Errorf("%w: server_version_num %d (%s); only PostgreSQL 11 to 18 is supported",
			ErrUnsupportedServer, num, version)
	}
	return nil
}

// serverGate checks the server once. A refusal is kept; a probe that fails
// (the server unreachable, say) is not, and the next call tries again.
type serverGate struct {
	db      *sql.DB
	mu      sync.Mutex
	checked bool
	err     error
}

func (g *serverGate) check(ctx context.Context) error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.checked {
		return g.err
	}
	var num int
	var version string
	var hologres bool
	if err := g.db.QueryRowContext(ctx, `SELECT current_setting('server_version_num')::int, version(),
		to_regproc('hg_version') IS NOT NULL`).Scan(&num, &version, &hologres); err != nil {
		return fmt.Errorf("postgres: read the server version: %w", err)
	}
	g.checked, g.err = true, checkServer(num, version, hologres)
	return g.err
}
