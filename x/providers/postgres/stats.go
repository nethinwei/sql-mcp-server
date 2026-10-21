package postgres

import (
	"cmp"
	"context"
	"slices"
	"time"
)

// relation is a table a plan reads, by schema (with VERBOSE) and name.
type relation struct{ schema, name string }

// columns are the schemas and the names of relations, deduplicated, as
// arrays a query unnests together.
func columns(relations []relation) (schemas, names []string) {
	relations = slices.Clone(relations)
	slices.SortFunc(relations, func(a, b relation) int {
		return cmp.Or(cmp.Compare(a.schema, b.schema), cmp.Compare(a.name, b.name))
	})
	for _, r := range slices.Compact(relations) {
		schemas, names = append(schemas, r.schema), append(names, r.name)
	}
	return schemas, names
}

// tableRowsSQL estimates the rows of the largest table the given relations
// ($1 schemas, any when empty; $2 names) read, a partition or an inherited
// table counting toward the table it belongs to (those of a partitioned
// table scanned whole add up), as the planner does: the analyzed rows per
// page times the pages the relation has now, so one that grew since it was
// analyzed counts as grown. One never analyzed counts as full pages of the
// smallest rows (a 28-byte tuple and line pointer). The count of relations
// found tells a name that is not a table.
const tableRowsSQL = `WITH RECURSIVE scanned AS (
  SELECT DISTINCT s.nsp, s.rel, c.oid
  FROM unnest($1::text[], $2::text[]) AS s(nsp, rel)
  JOIN pg_namespace n ON s.nsp = '' OR n.nspname = s.nsp
  JOIN pg_class c ON c.relnamespace = n.oid AND c.relname = s.rel AND c.relkind IN ('r', 'm')
), up(oid, root) AS (
  SELECT oid, oid FROM scanned
  UNION
  SELECT up.oid, i.inhparent FROM up JOIN pg_inherits i ON i.inhrelid = up.root
), sized AS (
  SELECT up.root, CASE WHEN c.relpages > 0 AND c.reltuples >= 0 THEN c.reltuples / c.relpages * p.pages
    ELSE p.pages * (current_setting('block_size')::float8 / 28) END AS rows
  FROM (SELECT DISTINCT oid, root FROM up) up
  JOIN pg_class c ON c.oid = up.oid,
  LATERAL (SELECT ceil(pg_relation_size(c.oid) / current_setting('block_size')::float8) AS pages) p
  WHERE NOT EXISTS (SELECT 1 FROM pg_inherits i WHERE i.inhrelid = up.root)
)
SELECT (SELECT count(DISTINCT (nsp, rel)) FROM scanned),
  coalesce((SELECT max(total) FROM (SELECT sum(rows) AS total FROM sized GROUP BY root) t), 0)::bigint`

// tableRows reports how many rows the largest table the relations read
// holds (see tableRowsSQL); false when that cannot be told.
func (e pgExplainer) tableRows(ctx context.Context, relations []relation) (int64, bool) {
	schemas, names := columns(relations)
	var found, rows int64
	if err := e.pool.DB().QueryRowContext(ctx, tableRowsSQL, schemas, names).Scan(&found, &rows); err != nil ||
		found != int64(len(names)) {
		return 0, false
	}
	return rows, true
}

// statsFreshSQL tells, for each of the relations ($1 schemas, any when
// empty; $2 names) that is a table, whether the planner's statistics of it
// can be trusted: it was analyzed, by ANALYZE or by autovacuum, or it is so
// small (at most $3 pages) that an estimate cannot be far off. Every table
// of the name counts when the schema is not known.
const statsFreshSQL = `SELECT s.nsp, s.rel, bool_and(coalesce(st.last_analyze, st.last_autoanalyze) IS NOT NULL
  OR pg_relation_size(c.oid) <= $3 * current_setting('block_size')::bigint)
FROM unnest($1::text[], $2::text[]) AS s(nsp, rel)
JOIN pg_namespace n ON s.nsp = '' OR n.nspname = s.nsp
JOIN pg_class c ON c.relnamespace = n.oid AND c.relname = s.rel AND c.relkind IN ('r', 'm')
LEFT JOIN pg_stat_all_tables st ON st.relid = c.oid
GROUP BY s.nsp, s.rel`

// smallTablePages is the size, in pages, up to which a table's statistics
// do not matter (64 KiB with the default 8 KiB pages).
const smallTablePages = 8

// statsFresh reports whether the statistics of every table the relations
// name are fresh (see statsFreshSQL), probing in one query those not kept.
func (e pgExplainer) statsFresh(ctx context.Context, relations []relation) bool {
	var probe []relation
	for _, r := range relations {
		at, ok := e.freshAt(r)
		if !ok || time.Since(at) >= statsTTL {
			probe = append(probe, r)
		}
	}
	if len(probe) == 0 {
		return true
	}
	stale, answered := e.queryStale(ctx, probe)
	// Only fresh is kept: a table once analyzed stays so, while one not yet
	// is told at once when ANALYZE runs. A probe that failed is asked again.
	if e.fresh != nil && answered {
		now := time.Now()
		for _, r := range probe {
			if !stale[r] {
				e.fresh.Store(r, now)
			}
		}
	}
	return len(stale) == 0
}

func (e pgExplainer) freshAt(r relation) (time.Time, bool) {
	if e.fresh == nil {
		return time.Time{}, false
	}
	at, ok := e.fresh.Load(r)
	if !ok {
		return time.Time{}, false
	}
	return at.(time.Time), true
}

// queryStale lists the relations whose statistics are stale, and whether
// the database answered. A probe that fails is permissive rather than
// blocking queries, and so is a relation that is no table (a view, a
// foreign table).
func (e pgExplainer) queryStale(ctx context.Context, relations []relation) (map[relation]bool, bool) {
	schemas, names := columns(relations)
	rows, err := e.pool.DB().QueryContext(ctx, statsFreshSQL, schemas, names, smallTablePages)
	if err != nil {
		return nil, false
	}
	defer func() { _ = rows.Close() }()
	stale := map[relation]bool{}
	for rows.Next() {
		var r relation
		var fresh bool
		if err := rows.Scan(&r.schema, &r.name, &fresh); err != nil {
			return nil, false
		}
		if !fresh {
			stale[r] = true
		}
	}
	if rows.Err() != nil {
		return nil, false
	}
	return stale, true
}
