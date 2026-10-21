package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/codegen"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/x/providers/sqladapter"
)

// ErrAnalyzeRequiresReadOnly protects the provider boundary from executing
// EXPLAIN ANALYZE for statements not marked read-only by codegen.
var ErrAnalyzeRequiresReadOnly = errors.New("postgres: EXPLAIN ANALYZE requires a codegen read-only query")

// pgExplainer estimates a plan by running EXPLAIN (FORMAT JSON).
type pgExplainer struct {
	pool *sqladapter.Pool
	// fresh keeps the tables statsFresh found fresh, for statsTTL: every
	// read of them asks, each time a query.
	fresh *sync.Map
}

// statsTTL is how long statsFresh's answer for a table is kept.
const statsTTL = time.Minute

// Explain implements cost.Explainer. It wraps the query in EXPLAIN and parses
// the JSON plan, walking the plan tree to detect sorts, temporary/hash nodes,
// and partition pruning. Statistics freshness is probed for the scanned
// table (see statsFresh); VERBOSE names its schema. A plan that does not
// parse degrades to ScanUnknown; one that cannot be read is an error,
// classified as the statement's (see sqladapter.Pool.Explain).
func (e pgExplainer) Explain(ctx context.Context, query string, args []any) (cost.Plan, error) {
	raw, err := e.pool.Explain(ctx, "EXPLAIN (FORMAT JSON, VERBOSE) "+query, args, "")
	if err != nil {
		return cost.Plan{}, err
	}
	if raw == "" {
		return cost.Plan{ScanType: cost.ScanUnknown}, nil
	}
	p, root, err := parsePGExplain([]byte(raw))
	if err != nil {
		return p, err
	}
	if scanned, ok := relationForScan(root, p.ScanType); ok {
		p.StatsFresh = e.statsFresh(ctx, scanned.Schema, scanned.RelationName)
	}
	if p.ScanType == cost.ScanFull {
		if relations, ok := fullScanRelations(root, nil); ok {
			p.TableRows, p.TableSized = e.tableRows(ctx, relations)
		}
	}
	return p, nil
}

// fullScanRelations lists the tables the plan's sequential scans read; false
// when a scan names none.
func fullScanRelations(n pgPlanNode, out []string) ([]string, bool) {
	if pgScanType(n.NodeType) == cost.ScanFull {
		if n.RelationName == "" {
			return nil, false
		}
		out = append(out, n.RelationName)
	}
	for _, child := range n.Plans {
		var ok bool
		if out, ok = fullScanRelations(child, out); !ok {
			return nil, false
		}
	}
	return out, true
}

// tableRowsSQL estimates the rows of the largest table of the given names (of
// any schema: a name in several counts as its largest) as the planner does:
// the analyzed rows per page times the pages the table has now, so a table
// that grew since it was analyzed counts as grown. A table never analyzed
// counts as full pages of the smallest rows (a 28-byte tuple and line
// pointer). The count of names found tells a name that is not a table.
const tableRowsSQL = `SELECT count(DISTINCT relname), coalesce(max(CASE
  WHEN relpages > 0 AND reltuples >= 0 THEN reltuples / relpages * pages
  ELSE pages * (block / 28) END), 0)::bigint
FROM (SELECT c.relname, c.relpages, c.reltuples,
  ceil(pg_relation_size(c.oid) / current_setting('block_size')::float8) AS pages,
  current_setting('block_size')::float8 AS block
  FROM pg_class c WHERE c.relname = ANY($1) AND c.relkind IN ('r', 'm')) t`

// tableRows reports how many rows the largest of the relations holds; false
// when that cannot be told.
func (e pgExplainer) tableRows(ctx context.Context, relations []string) (int64, bool) {
	slices.Sort(relations)
	relations = slices.Compact(relations)
	var found, rows int64
	if err := e.pool.DB().QueryRowContext(ctx, tableRowsSQL, relations).Scan(&found, &rows); err != nil ||
		found != int64(len(relations)) {
		return 0, false
	}
	return rows, true
}

// statsFreshSQL tells whether the planner's statistics of a table can be
// trusted: it was analyzed, by ANALYZE or by autovacuum, or it is so small
// (at most smallTablePages pages) that an estimate cannot be far off.
// Autovacuum never analyzes a table of fewer rows than its threshold, so a
// small table may well have never been. Every table of the name counts when
// the schema is not known.
const statsFreshSQL = `SELECT bool_and(coalesce(s.last_analyze, s.last_autoanalyze) IS NOT NULL
  OR pg_relation_size(c.oid) <= $3 * current_setting('block_size')::bigint)
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_stat_all_tables s ON s.relid = c.oid
WHERE c.relname = $1 AND ($2 = '' OR n.nspname = $2) AND c.relkind IN ('r', 'm')`

// smallTablePages is the size, in pages, up to which a table's statistics
// do not matter (64 KiB with the default 8 KiB pages).
const smallTablePages = 8

// statsFresh reports whether the statistics of the table schema.rel are
// fresh (see statsFreshSQL).
func (e pgExplainer) statsFresh(ctx context.Context, schema, rel string) bool {
	key := schema + "\x00" + rel
	if e.fresh != nil {
		if at, ok := e.fresh.Load(key); ok && time.Since(at.(time.Time)) < statsTTL {
			return true
		}
	}
	got, answered := e.queryStatsFresh(ctx, schema, rel)
	// Only fresh is kept: a table once analyzed stays so, while one not yet
	// is told at once when ANALYZE runs. A probe that failed is asked again.
	if e.fresh != nil && got && answered {
		e.fresh.Store(key, time.Now())
	}
	return got
}

// queryStatsFresh probes the statistics of schema.rel, and whether the
// database answered.
func (e pgExplainer) queryStatsFresh(ctx context.Context, schema, rel string) (fresh, answered bool) {
	var got sql.NullBool
	if err := e.pool.DB().QueryRowContext(ctx, statsFreshSQL, rel, schema, smallTablePages).Scan(&got); err != nil {
		// A probe that fails is permissive rather than blocking queries.
		return true, false
	}
	// No such table (a view, a foreign table): permissive too.
	return !got.Valid || got.Bool, true
}

type pgPlanNode struct {
	NodeType        string       `json:"Node Type"`
	RelationName    string       `json:"Relation Name"`
	Schema          string       `json:"Schema"` // with VERBOSE
	TotalCost       float64      `json:"Total Cost"`
	PlanRows        int64        `json:"Plan Rows"`
	ActualRows      float64      `json:"Actual Rows"` // an average per loop since PostgreSQL 18
	ActualLoops     int64        `json:"Actual Loops"`
	SubplansRemoved int          `json:"Subplans Removed"`
	Plans           []pgPlanNode `json:"Plans"`
}

type pgExplainRow struct {
	Plan          pgPlanNode `json:"Plan"`
	ExecutionTime float64    `json:"Execution Time"`
}

// ExplainAnalyze implements cost.AnalyzeSampler. EXPLAIN ANALYZE executes the
// compiled read-only statement once more; callers must apply a separate,
// tightly bounded sampling context.
func (p *Provider) ExplainAnalyze(ctx context.Context, compiled codegen.Compiled) (plan cost.Plan, err error) {
	if !compiled.ReadOnly {
		return cost.Plan{}, ErrAnalyzeRequiresReadOnly
	}
	beginner := p.analyzeTx
	if beginner == nil {
		beginner = p
	}
	tx, err := beginner.BeginTx(ctx, &store.TxOptions{ReadOnly: true})
	if err != nil {
		return cost.Plan{}, err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); err == nil && rollbackErr != nil {
			err = rollbackErr
		}
	}()
	rows, err := tx.QueryContext(ctx,
		"EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+compiled.SQL,
		compiled.Args...,
	)
	if err != nil {
		return cost.Plan{}, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return cost.Plan{}, err
		}
		return cost.Plan{}, errors.New("postgres: EXPLAIN ANALYZE returned no plan")
	}
	var raw any
	if err := rows.Scan(&raw); err != nil {
		return cost.Plan{}, err
	}
	var encoded []byte
	switch value := raw.(type) {
	case string:
		encoded = []byte(value)
	case []byte:
		encoded = value
	default:
		return cost.Plan{}, fmt.Errorf("postgres: unexpected EXPLAIN ANALYZE result %T", raw)
	}
	return parsePGExplainAnalyze(encoded)
}

func parsePGExplainAnalyze(b []byte) (cost.Plan, error) {
	p, _, err := parsePGExplain(b)
	if err != nil {
		return p, err
	}
	var arr []pgExplainRow
	if err := json.Unmarshal(b, &arr); err != nil {
		return cost.Plan{
				ScanType: cost.ScanUnknown,
				Raw:      b,
			}, fmt.Errorf(
				"postgres: parse EXPLAIN ANALYZE JSON: %w",
				err,
			)
	}
	if len(arr) == 0 {
		return cost.Plan{ScanType: cost.ScanUnknown, Raw: b}, errors.New("postgres: empty EXPLAIN ANALYZE plan")
	}
	p.ActualRows = int64(math.Round(actualRowsProcessed(arr[0].Plan)))
	p.ExecutionTime = time.Duration(arr[0].ExecutionTime * float64(time.Millisecond))
	return p, nil
}

// actualRowsProcessed sums the rows of every node: its rows per loop (an
// average, fractional since PostgreSQL 18) times its loops.
func actualRowsProcessed(node pgPlanNode) float64 {
	rows := node.ActualRows * float64(node.ActualLoops)
	for _, child := range node.Plans {
		rows += actualRowsProcessed(child)
	}
	return rows
}

// parsePGExplain parses EXPLAIN (FORMAT JSON) output, walking the plan tree to
// fill HasSort/HasTempTable/PartitionPruned. It returns the plan and the root
// node (for stats probing). On any structural surprise it returns ScanUnknown.
func parsePGExplain(b []byte) (cost.Plan, pgPlanNode, error) {
	var arr []pgExplainRow
	if err := json.Unmarshal(b, &arr); err != nil {
		return cost.Plan{ScanType: cost.ScanUnknown, Raw: b}, pgPlanNode{}, nil
	}
	if len(arr) == 0 {
		return cost.Plan{ScanType: cost.ScanUnknown, Raw: b}, pgPlanNode{}, nil
	}
	root := arr[0].Plan
	p := cost.Plan{
		TotalCost:     root.TotalCost,
		EstimatedRows: root.PlanRows,
		ScanType:      pgScanType(root.NodeType),
		StatsFresh:    true,
		Raw:           b,
	}
	walkPlan(root, &p)
	return p, root, nil
}

// tempRows is the size from which a hashed or materialized input counts as
// a temporary table.
const tempRows = 10_000

// walkPlan recurses the plan tree, retaining the riskiest concrete scan and
// the largest row estimate rather than classifying only a wrapper root node.
func walkPlan(n pgPlanNode, p *cost.Plan) {
	if scan := pgScanType(n.NodeType); scan != cost.ScanUnknown &&
		(p.ScanType == cost.ScanUnknown || cost.Worse(scan, p.ScanType)) {
		p.ScanType = scan
	}
	if n.PlanRows > p.EstimatedRows {
		p.EstimatedRows = n.PlanRows
	}
	switch n.NodeType {
	case "Sort", "Incremental Sort":
		p.HasSort = true
	case "Materialize", "Hash":
		// A hash or materialized input held in memory; that of a small
		// relation (a lookup table joined) costs nothing worth scoring.
		p.HasTempTable = p.HasTempTable || n.PlanRows > tempRows
	}
	if n.SubplansRemoved > 0 {
		p.PartitionPruned = true
	}
	for _, sub := range n.Plans {
		walkPlan(sub, p)
	}
}

func relationForScan(node pgPlanNode, scan cost.ScanType) (pgPlanNode, bool) {
	if pgScanType(node.NodeType) == scan && node.RelationName != "" {
		return node, true
	}
	for _, child := range node.Plans {
		if scanned, ok := relationForScan(child, scan); ok {
			return scanned, true
		}
	}
	return pgPlanNode{}, false
}

func pgScanType(node string) cost.ScanType {
	switch {
	case strings.Contains(node, "Seq Scan"):
		// PostgreSQL Seq Scan reads the whole heap: treat as a full scan so
		// RejectFullScan catches unfiltered queries.
		return cost.ScanFull
	case node == "Bitmap Heap Scan":
		// The heap pages its bitmap index scans found: driven by an index,
		// but as many pages as the index condition matches, maybe most of
		// the table. Its child, a Bitmap Index Scan, is no bounded read of
		// it, so the plan counts as worse than an index scan (a bitmap is
		// also how an IN list of keys is read, so it is no full scan).
		return cost.ScanSeq
	case strings.Contains(node, "Index Only Scan"):
		return cost.ScanIndex
	case strings.Contains(node, "Index Scan"):
		return cost.ScanIndex
	case strings.Contains(node, "Tid Scan"):
		return cost.ScanPoint
	case strings.Contains(node, "Tid Range Scan"):
		// A range of a table's row locations (PostgreSQL 14), read in heap
		// order: it may span the whole table, so it is no bounded index read.
		return cost.ScanSeq
	case node == "Result" || node == "Values Scan":
		// No table is read: a constant SELECT, or the rows INSERT ... VALUES
		// writes. A scan elsewhere in the plan still outranks it.
		return cost.ScanPoint
	}
	return cost.ScanUnknown
}
