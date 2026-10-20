//go:build integration

package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/internal/pgimage"
	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
	pgprov "github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

// An unsupported server is refused on every connection, before any statement
// runs on it: at open, and, opened lazily (as serve does), on the first
// statement of any kind, a write too, which then did not run.
func TestPGRefusesUnsupportedServersOnEveryConnection(t *testing.T) {
	if pgimage.Image() != pgimage.Default {
		t.Skip("the refusal does not depend on the supported version tested; it runs on the default image")
	}
	ctx := context.Background()
	container, err := postgres.Run(ctx, "postgres:10-alpine", postgres.WithDatabase("test"),
		postgres.WithUsername("test"), postgres.WithPassword("test"), postgres.BasicWaitStrategies())
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	defer func() { _ = container.Terminate(context.Background()) }()
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	opts := providerregistry.Options{Timeout: 30 * time.Second}
	if _, err := pgprov.Open(dsn, opts); !errors.Is(err, pgprov.ErrUnsupportedServer) {
		t.Fatalf("Open = %v, want ErrUnsupportedServer", err)
	}
	opts.Lazy = true
	prov, err := pgprov.Open(dsn, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = prov.Close() }()
	_, err = prov.ExecContext(ctx, `CREATE TABLE written (id integer)`)
	if !errors.Is(err, pgprov.ErrUnsupportedServer) || !errors.Is(err, store.ErrNotConnected) {
		t.Fatalf("a write on a lazily opened provider = %v, want the server refused, not connected", err)
	}
	raw, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	var table sql.NullString
	if err := raw.QueryRowContext(ctx, `SELECT to_regclass('written')::text`).Scan(&table); err != nil || table.Valid {
		t.Fatalf("the refused write ran: table %v (%v)", table, err)
	}
}

// A user's own function named like Hologres's hg_version() does not make
// PostgreSQL pass for Hologres.
func TestPGServesAServerWithAUserHgVersion(t *testing.T) {
	dsn, stop := startPG(t)
	defer stop()
	prov, err := pgprov.New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = prov.Close() }()
	if _, err := prov.ExecContext(context.Background(),
		`CREATE FUNCTION public.hg_version() RETURNS text LANGUAGE sql AS 'SELECT ''3.0'''`); err != nil {
		t.Fatal(err)
	}
	again, err := pgprov.New(dsn)
	if err != nil {
		t.Fatalf("PostgreSQL with a user's hg_version(): %v", err)
	}
	_ = again.Close()
}
