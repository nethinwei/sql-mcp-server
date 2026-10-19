// Package configstore persists configuration revisions in SQLite or in a
// PostgreSQL, MySQL or OceanBase database. Every table uses the smcp_ prefix;
// the store keeps its own schema version and fails closed on an unknown one.
package configstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
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
// added administrator accounts, version 3 the scanned catalog, plan templates
// and administrator tokens, version 4 token scopes and scan batch items as
// rows, version 5 administrators' workspaces, version 6 the database
// constraint of at most one published revision.
const SchemaVersion = 6

// Errors returned when opening a store.
var (
	ErrInvalidSpec    = errors.New("configstore: invalid store spec")
	ErrNotInitialized = errors.New("configstore: store is not initialized; run `sql-mcp-server store init`")
	ErrNewerSchema    = errors.New("configstore: store schema is newer than this build")
	// ErrStoreCorrupt is a store whose rows break its invariants: a missing
	// lock or counter row, or more than one published revision.
	ErrStoreCorrupt = errors.New("configstore: store is corrupt")
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
// when the tables or metadata rows are missing (an Init that stopped part
// way), with ErrStoreCorrupt when a store with revisions lost one, and with
// ErrNewerSchema on a newer layout.
func Open(ctx context.Context, spec Spec, resolve Resolver) (*SQLStore, error) {
	s, err := connect(spec, resolve)
	if err != nil {
		return nil, err
	}
	version, err := s.checkSchema(ctx)
	if err == nil && version < SchemaVersion {
		err = s.migrate(ctx, version)
	}
	if err == nil {
		var missing [][2]any
		if missing, err = s.missingMeta(ctx, s.db); err == nil && len(missing) > 0 {
			err = fmt.Errorf("%w (metadata row %v missing)", ErrNotInitialized, missing[0][0])
		}
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
			if check, ok := schemaChecks[v]; ok {
				if err := check(ctx, s, tx); err != nil {
					return err
				}
			}
			if err := s.dialect.runDDL(ctx, tx, s.dialect.migrations[v-1]); err != nil {
				return err
			}
			if move, ok := dataMigrations[v]; ok {
				if err := move(ctx, s, tx); err != nil {
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

// schemaChecks refuse a migration the store's rows would break; each runs in
// the transaction of its version, before its DDL.
var schemaChecks = map[int64]func(ctx context.Context, s *SQLStore, tx *sql.Tx) error{
	6: onePublished,
}

// onePublished refuses a store holding more than one published revision,
// which version 6 forbids, naming them and the repair.
func onePublished(ctx context.Context, s *SQLStore, tx *sql.Tx) error {
	ids, err := queryAll(ctx, s, tx, func(row scanner) (id int64, err error) {
		return id, row.Scan(&id)
	}, "SELECT id FROM smcp_revisions WHERE state = ? ORDER BY id", string(revision.StatePublished))
	if err != nil || len(ids) <= 1 {
		return err
	}
	list := make([]string, len(ids))
	for i, id := range ids {
		list[i] = strconv.FormatInt(id, 10)
	}
	return fmt.Errorf("%w: revisions %s are all published; keep the one the servers run and mark the others "+
		"superseded (UPDATE smcp_revisions SET state = 'superseded' WHERE id IN (...)), then open the store again",
		ErrStoreCorrupt, strings.Join(list, ", "))
}

// Init creates the store tables when absent; it is a no-op on an initialized
// store of the same version. An Init that stopped part way runs again: the
// tables are created if absent and the missing metadata rows written.
// Inits running at the same time, in any process, run one after another
// (see dialect.lockInit): each after the first finds the store initialized.
func Init(ctx context.Context, spec Spec, resolve Resolver) error {
	s, err := connect(spec, resolve)
	if err != nil {
		return err
	}
	defer func() { _ = s.db.Close() }()
	if s.dialect.lockInit != nil {
		conn, err := s.db.Conn(ctx)
		if err != nil {
			return fmt.Errorf("configstore: connect: %w", err)
		}
		defer func() { _ = conn.Close() }()
		unlock, err := s.dialect.lockInit(ctx, conn)
		if err != nil {
			return fmt.Errorf("configstore: lock the store for init: %w", err)
		}
		defer unlock()
	}
	return s.init(ctx)
}

func (s *SQLStore) init(ctx context.Context) error {
	version, err := s.checkSchema(ctx)
	if err == nil {
		if version < SchemaVersion {
			if err := s.migrate(ctx, version); err != nil {
				return err
			}
		}
		return s.initMeta(ctx)
	}
	if !errors.Is(err, ErrNotInitialized) {
		return err
	}
	for _, step := range s.dialect.migrations {
		if err := s.dialect.runDDL(ctx, s.db, step); err != nil {
			return fmt.Errorf("configstore: create tables: %w", err)
		}
	}
	return s.initMeta(ctx)
}

// missingMeta returns the metadata rows the store lacks, with their first
// values, the schema version last. Those values hold only while the store
// has no revision; one that has revisions and lacks a row lost it, and is
// corrupt.
func (s *SQLStore) missingMeta(ctx context.Context, q execer) ([][2]any, error) {
	keys, err := queryAll(ctx, s, q, func(row scanner) (key string, err error) {
		return key, row.Scan(&key)
	}, "SELECT meta_key FROM smcp_store_meta")
	if err != nil {
		return nil, err
	}
	rows := [][2]any{{metaNextID, 1}, {metaPublishLock, 0}, {metaRevisionSeq, 0},
		{metaSchemaVersion, SchemaVersion}}
	rows = slices.DeleteFunc(rows, func(row [2]any) bool { return slices.Contains(keys, row[0].(string)) })
	if len(rows) == 0 {
		return nil, nil
	}
	var revisions int64
	if err := s.queryRow(ctx, q, "SELECT COUNT(*) FROM smcp_revisions").Scan(&revisions); err != nil {
		return nil, err
	}
	if revisions > 0 {
		return nil, fmt.Errorf("%w: store metadata row %v missing from a store with revisions; restore it from "+
			"a backup", ErrStoreCorrupt, rows[0][0])
	}
	return rows, nil
}

// initMeta writes the metadata rows the store lacks (see missingMeta) in one
// transaction, the schema version last: a store has its version only with
// the others.
func (s *SQLStore) initMeta(ctx context.Context) error {
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		// Write before reading: SQLite then takes its write lock first, waiting
		// for another Init's, instead of failing to upgrade a read (SQLITE_BUSY).
		if _, err := s.exec(ctx, tx, "UPDATE smcp_store_meta SET meta_value = meta_value WHERE 1 = 0"); err != nil {
			return err
		}
		rows, err := s.missingMeta(ctx, tx)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if _, err := s.exec(ctx, tx, "INSERT INTO smcp_store_meta (meta_key, meta_value) VALUES (?, ?)",
				row[0], row[1]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("configstore: initialize metadata: %w", err)
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
	// metaRevisionSeq counts the revision writes other than creating one
	// (publish, rollback, discard, import); see SQLStore.Sequence.
	metaRevisionSeq = "revision_seq"
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
