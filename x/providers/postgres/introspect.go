package postgres

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
)

// pgIntrospector discovers entities from information_schema.
type pgIntrospector struct {
	db *sql.DB
}

// Schemas implements introspect.SchemaLister: every schema except the system
// ones, and the current schema (the first one of search_path that exists).
func (i pgIntrospector) Schemas(ctx context.Context) ([]string, string, error) {
	rows, err := i.db.QueryContext(ctx,
		`SELECT schema_name FROM information_schema.schemata
		 WHERE schema_name NOT IN ('pg_catalog', 'information_schema')
		   AND schema_name NOT LIKE 'pg\_toast%' AND schema_name NOT LIKE 'pg\_temp\_%'
		 ORDER BY schema_name`)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, "", err
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	current, err := i.currentSchema(ctx)
	return out, current, err
}

func (i pgIntrospector) currentSchema(ctx context.Context) (string, error) {
	var current sql.NullString
	err := i.db.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&current)
	return current.String, err
}

// pgKind maps a pg_class relkind to the entity kind.
func pgKind(relkind string) entity.Kind {
	if relkind == "v" || relkind == "m" {
		return entity.KindView
	}
	return entity.KindTable
}

// uniqueKeyReason says why a unique index cannot identify a row, or "".
func uniqueKeyReason(partial, expression, nullsDistinctFree bool) string {
	switch {
	case partial:
		return entity.KeyPartial
	case expression:
		return entity.KeyExpression
	case !nullsDistinctFree:
		return entity.KeyNullable
	}
	return ""
}

// pgAction maps pg_constraint confdeltype/confupdtype codes; a (no action)
// and r (restrict) only check and map to "".
var pgAction = map[string]string{"c": entity.FKCascade, "n": entity.FKSetNull, "d": entity.FKSetDefault}

// Physical implements introspect.PhysicalNamer. The cluster's system
// identifier names the server; a role that may not read it falls back to the
// server address, and to "" (unknown) over a Unix socket: the datasource then
// names its relations alone, rather than its assembly failing. Any other
// error fails, as a datasource named by the wrong key would expose a relation
// another one does without its being seen (I-1).
func (i pgIntrospector) Physical(ctx context.Context) (introspect.Physical, error) {
	var p introspect.Physical
	if err := i.db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&p.Catalog); err != nil {
		return p, err
	}
	err := i.db.QueryRowContext(ctx, `SELECT system_identifier::text FROM pg_control_system()`).Scan(&p.Server)
	if err == nil {
		return p, nil
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" && pgErr.Code != "42883" {
		return p, err
	}
	var addr sql.NullString
	if err := i.db.QueryRowContext(ctx,
		`SELECT host(inet_server_addr()) || ':' || inet_server_port()`).Scan(&addr); err != nil {
		return p, err
	}
	if !addr.Valid {
		slog.Warn("postgres: the server cannot be identified; its relations are named by the datasource")
	}
	p.Server = addr.String
	return p, nil
}
