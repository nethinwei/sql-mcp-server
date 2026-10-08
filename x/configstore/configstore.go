// Package configstore persists configuration revisions in SQLite or in a
// PostgreSQL, MySQL or OceanBase database. Every table uses the smcp_ prefix;
// the store keeps its own schema version and fails closed on an unknown one.
package configstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	// Database drivers for the supported store backends.
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/revision"
)

// TablePrefix prefixes every store table; see config.StoreTablePrefix.
const TablePrefix = config.StoreTablePrefix

// SchemaVersion is the store table layout this build understands. Version 2
// added administrator accounts.
const SchemaVersion = 2

// Errors returned when opening a store.
var (
	ErrInvalidSpec    = errors.New("configstore: invalid store spec")
	ErrNotInitialized = errors.New("configstore: store is not initialized; run `sql-mcp-server store init`")
	ErrNewerSchema    = errors.New("configstore: store schema is newer than this build")
)

// Spec is a parsed "<driver>:<dsn>" store location.
type Spec struct {
	Driver string // sqlite | postgres | mysql | oceanbase
	DSN    string
}

// ParseSpec parses "<driver>:<dsn>". The DSN may contain ${ENV} or
// ${file:/path} placeholders, resolved by Open.
func ParseSpec(raw string) (Spec, error) {
	driver, dsn, ok := strings.Cut(strings.TrimSpace(raw), ":")
	if !ok || dsn == "" {
		return Spec{}, fmt.Errorf("%w: %q, want <driver>:<dsn>", ErrInvalidSpec, raw)
	}
	if _, known := dialects[driver]; !known {
		return Spec{}, fmt.Errorf("%w: unknown driver %q (sqlite, postgres, mysql, oceanbase)", ErrInvalidSpec, driver)
	}
	return Spec{Driver: driver, DSN: dsn}, nil
}

// String returns the spec without its DSN, safe for logs.
func (s Spec) String() string { return s.Driver + ":<redacted>" }

// Resolver expands DSN placeholders.
type Resolver func(string) (string, error)

// Open connects to an initialized store. It fails with ErrNotInitialized
// when the tables are missing and with ErrNewerSchema on a newer layout.
func Open(ctx context.Context, spec Spec, resolve Resolver) (*SQLStore, error) {
	s, err := connect(spec, resolve)
	if err != nil {
		return nil, err
	}
	version, err := s.checkSchema(ctx)
	if err == nil && version < SchemaVersion {
		err = s.migrate(ctx, version)
	}
	if err != nil {
		_ = s.db.Close()
		return nil, err
	}
	return s, nil
}

// migrate upgrades the store schema from version to SchemaVersion, one
// version at a time, recording each step in smcp_store_meta.
func (s *SQLStore) migrate(ctx context.Context, version int64) error {
	for v := version + 1; v <= SchemaVersion; v++ {
		err := s.withTx(ctx, func(tx *sql.Tx) error {
			for _, ddl := range s.dialect.migrations[v-1] {
				if _, err := tx.ExecContext(ctx, ddl); err != nil {
					return err
				}
			}
			_, err := s.exec(ctx, tx, "UPDATE smcp_store_meta SET meta_value = ? WHERE meta_key = ?",
				v, metaSchemaVersion)
			return err
		})
		if err != nil {
			return fmt.Errorf("configstore: migrate schema to version %d: %w", v, err)
		}
	}
	return nil
}

// Init creates the store tables when absent; it is a no-op on an initialized
// store of the same version.
func Init(ctx context.Context, spec Spec, resolve Resolver) error {
	s, err := connect(spec, resolve)
	if err != nil {
		return err
	}
	defer func() { _ = s.db.Close() }()
	version, err := s.checkSchema(ctx)
	if err == nil {
		if version < SchemaVersion {
			return s.migrate(ctx, version)
		}
		return nil
	}
	if !errors.Is(err, ErrNotInitialized) {
		return err
	}
	for _, step := range s.dialect.migrations {
		for _, ddl := range step {
			if _, err := s.db.ExecContext(ctx, ddl); err != nil {
				return fmt.Errorf("configstore: create tables: %w", err)
			}
		}
	}
	for _, row := range [][2]any{{metaSchemaVersion, SchemaVersion}, {metaNextID, 1}, {metaPublishLock, 0}} {
		if _, err := s.exec(ctx, s.db, "INSERT INTO smcp_store_meta (meta_key, meta_value) VALUES (?, ?)",
			row[0], row[1]); err != nil {
			return fmt.Errorf("configstore: initialize metadata: %w", err)
		}
	}
	return nil
}

func connect(spec Spec, resolve Resolver) (*SQLStore, error) {
	d, ok := dialects[spec.Driver]
	if !ok {
		return nil, fmt.Errorf("%w: unknown driver %q", ErrInvalidSpec, spec.Driver)
	}
	dsn := spec.DSN
	if resolve != nil {
		resolved, err := resolve(dsn)
		if err != nil {
			return nil, fmt.Errorf("configstore: resolve dsn: %w", err)
		}
		dsn = resolved
	}
	db, err := sql.Open(d.driver, d.prepareDSN(dsn))
	if err != nil {
		return nil, fmt.Errorf("configstore: open %s: %w", spec, err)
	}
	db.SetMaxOpenConns(4)
	return &SQLStore{db: db, dialect: d, now: defaultNow}, nil
}

const (
	metaSchemaVersion = "schema_version"
	metaNextID        = "next_id"
	metaPublishLock   = "publish_lock"
)

func (s *SQLStore) checkSchema(ctx context.Context) (int64, error) {
	var version int64
	err := s.queryRow(ctx, s.db, "SELECT meta_value FROM smcp_store_meta WHERE meta_key = ?",
		metaSchemaVersion).Scan(&version)
	if err != nil {
		if pingErr := s.db.PingContext(ctx); pingErr != nil {
			return 0, fmt.Errorf("configstore: connect: %w", pingErr)
		}
		return 0, ErrNotInitialized
	}
	if version > SchemaVersion {
		return version, fmt.Errorf("%w: store has version %d, this build supports %d",
			ErrNewerSchema, version, SchemaVersion)
	}
	return version, nil
}

var _ revision.Store = (*SQLStore)(nil)
