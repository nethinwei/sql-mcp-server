package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// The servers this provider is built for are PostgreSQL itself, 11 (whose
// pg_index has indnkeyatts) to 18. A server that speaks its protocol but is
// of another kind differs in its catalogs, plans, transactions or RETURNING,
// so its connections are refused (see checkConn) rather than served half
// right; docs/supported-versions.md says the same.
const (
	minServerVersion = 110000
	maxServerVersion = 190000 // exclusive
)

// ErrUnsupportedServer is a server outside the versions and kind supported.
var ErrUnsupportedServer = errors.New("postgres: unsupported server")

// derivatives are the markers in version() of servers that speak the
// PostgreSQL protocol but are not PostgreSQL. YugabyteDB names itself in the
// version number (11.2-YB-2.6.1.0), not always in the rest.
var derivatives = []string{
	"hologres", "greenplum", "redshift", "cockroachdb", "opengauss", "gaussdb", "yugabyte", "-yb-",
}

// checkServer tells whether the server whose server_version_num is num and
// version() is version is supported; hologres is whether it has Hologres's
// hg_version() function: a built-in one (oid below FirstNormalObjectId) or
// one in pg_catalog, neither of which a user's own function can be.
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

// serverSQL reads what checkServer judges.
const serverSQL = `SELECT current_setting('server_version_num')::int, version(),
	EXISTS (SELECT FROM pg_proc WHERE proname = 'hg_version'
	  AND (oid < 16384 OR pronamespace = 'pg_catalog'::regnamespace))`

// checkConn checks the server of each connection as it is made, before
// any statement uses it: every connection of the pool, whatever it serves
// (reads, writes, procedures, raw SQL), and the server behind a connection
// made after a failover, a restart or an upgrade in place, is judged anew.
func checkConn(ctx context.Context, conn *pgx.Conn) error {
	var num int
	var version string
	var hologres bool
	if err := conn.QueryRow(ctx, serverSQL).Scan(&num, &version, &hologres); err != nil {
		return fmt.Errorf("postgres: read the server version: %w", err)
	}
	return checkServer(num, version, hologres)
}
