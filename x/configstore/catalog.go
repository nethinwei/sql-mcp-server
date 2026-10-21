package configstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nethinwei/sql-mcp-server/internal/kit"
	"github.com/nethinwei/sql-mcp-server/x/catalog"
)

// The scanned catalog is kept outside configuration revisions: it describes
// the databases, not the configuration, and a scan never needs a publish.

// CatalogList implements catalog.Store.
func (s *SQLStore) CatalogList(ctx context.Context, source string) (catalog.List, bool, error) {
	var payload string
	var listed int64
	err := s.queryRow(ctx, s.db, "SELECT payload, listed_us FROM smcp_catalog_lists WHERE source_id = ?", source).
		Scan(&payload, &listed)
	if err == sql.ErrNoRows {
		return catalog.List{}, false, nil
	}
	if err != nil {
		return catalog.List{}, false, fmt.Errorf("configstore: read schema list: %w", err)
	}
	var l catalog.List
	if err := json.Unmarshal([]byte(payload), &l); err != nil {
		return catalog.List{}, false, fmt.Errorf("configstore: decode schema list: %w", err)
	}
	l.ListedAt = fromMicros(listed)
	return l, true, nil
}

// PutCatalogList implements catalog.Store.
func (s *SQLStore) PutCatalogList(ctx context.Context, source, datasource string, l catalog.List) error {
	payload, err := json.Marshal(l)
	if err != nil {
		return err
	}
	_, err = s.exec(ctx, s.db,
		"INSERT INTO smcp_catalog_lists (source_id, datasource, payload, listed_us) VALUES (?, ?, ?, ?)"+
			s.dialect.upsert([]string{"source_id"}, "datasource", "payload", "listed_us"),
		source, datasource, string(payload), toMicros(l.ListedAt))
	return err
}

// CatalogVersions implements catalog.Store.
func (s *SQLStore) CatalogVersions(ctx context.Context, source string) ([]catalog.Version, error) {
	out, err := queryAll(ctx, s, s.db, func(row scanner) (v catalog.Version, err error) {
		var scanned int64
		err = row.Scan(&v.Name, &scanned, &v.Tables)
		v.ScannedAt = fromMicros(scanned)
		return v, err
	}, "SELECT schema_name, scanned_us, table_count FROM smcp_catalog_schemas WHERE source_id = ? ORDER BY schema_name",
		source)
	if err != nil {
		return nil, fmt.Errorf("configstore: list scanned schemas: %w", err)
	}
	return kit.OrEmpty(out), nil
}

// CatalogSchemas implements catalog.Store.
func (s *SQLStore) CatalogSchemas(ctx context.Context, source string, names []string) ([]catalog.Schema, error) {
	out := []catalog.Schema{}
	// Read in batches: databases bound the number of placeholders.
	for start := 0; start < len(names); start += 200 {
		batch := names[start:min(start+200, len(names))]
		args := []any{source}
		for _, n := range batch {
			args = append(args, n)
		}
		read, err := queryAll(ctx, s, s.db, scanSchema,
			"SELECT schema_name, payload, scanned_us FROM smcp_catalog_schemas WHERE source_id = ? AND schema_name IN ("+
				placeholders(len(batch))+") ORDER BY schema_name", args...)
		if err != nil {
			return nil, fmt.Errorf("configstore: read scanned schemas: %w", err)
		}
		out = append(out, read...)
	}
	return out, nil
}

func scanSchema(row scanner) (catalog.Schema, error) {
	var name, payload string
	var scanned int64
	if err := row.Scan(&name, &payload, &scanned); err != nil {
		return catalog.Schema{}, err
	}
	var sc catalog.Schema
	if err := json.Unmarshal([]byte(payload), &sc); err != nil {
		return catalog.Schema{}, fmt.Errorf("configstore: decode scanned schema %q: %w", name, err)
	}
	sc.Name, sc.ScannedAt = name, fromMicros(scanned)
	sc.Tables = kit.OrEmpty(sc.Tables)
	return sc, nil
}

// PutCatalogSchemas implements catalog.Store. Each schema is one upsert, in
// the order of their names, so concurrent puts of the same schemas wait on
// one another instead of failing on the key or deadlocking.
func (s *SQLStore) PutCatalogSchemas(ctx context.Context, source, datasource string, schemas []catalog.Schema) error {
	schemas = slices.SortedFunc(slices.Values(schemas),
		func(a, b catalog.Schema) int { return strings.Compare(a.Name, b.Name) })
	upsert := s.dialect.upsert([]string{"source_id", "schema_name"}, "datasource", "table_count", "payload", "scanned_us")
	return s.withTx(ctx, func(tx *sql.Tx) error {
		for _, sc := range schemas {
			payload, err := json.Marshal(sc)
			if err != nil {
				return err
			}
			if _, err := s.exec(ctx, tx, "INSERT INTO smcp_catalog_schemas "+
				"(source_id, schema_name, datasource, table_count, payload, scanned_us) VALUES (?, ?, ?, ?, ?, ?)"+upsert,
				source, sc.Name, datasource, len(sc.Tables), string(payload), toMicros(sc.ScannedAt)); err != nil {
				return err
			}
		}
		return nil
	})
}

// PruneCatalog implements catalog.Store.
func (s *SQLStore) PruneCatalog(ctx context.Context, datasource, keep string, before time.Time) error {
	us := toMicros(before)
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := s.exec(ctx, tx, "DELETE FROM smcp_catalog_lists "+
			"WHERE datasource = ? AND source_id <> ? AND listed_us < ?", datasource, keep, us); err != nil {
			return err
		}
		_, err := s.exec(ctx, tx, "DELETE FROM smcp_catalog_schemas "+
			"WHERE datasource = ? AND source_id <> ? AND scanned_us < ?", datasource, keep, us)
		return err
	})
}

// DeleteCatalogSchemas implements catalog.Store.
func (s *SQLStore) DeleteCatalogSchemas(ctx context.Context, source string, names []string, before time.Time) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		for _, name := range names {
			if _, err := s.exec(ctx, tx, "DELETE FROM smcp_catalog_schemas "+
				"WHERE source_id = ? AND schema_name = ? AND scanned_us < ?", source, name, toMicros(before)); err != nil {
				return err
			}
		}
		return nil
	})
}

var _ catalog.Store = (*SQLStore)(nil)
