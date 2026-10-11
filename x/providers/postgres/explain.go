package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/codegen"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/store"
)

// ErrAnalyzeRequiresReadOnly protects the provider boundary from executing
// EXPLAIN ANALYZE for statements not marked read-only by codegen.
var ErrAnalyzeRequiresReadOnly = errors.New("postgres: EXPLAIN ANALYZE requires a codegen read-only query")

// pgExplainer estimates a plan by running EXPLAIN (FORMAT JSON).
type pgExplainer struct {
	db *sql.DB
}

// Explain implements cost.Explainer. It wraps the query in EXPLAIN and parses
// the JSON plan, walking the plan tree to detect sorts, temporary/hash nodes,
// and partition pruning. Statistics freshness is probed from
// pg_stat_user_tables.last_analyze. Any parse failure degrades to ScanUnknown.
func (e pgExplainer) Explain(ctx context.Context, query string, args []any) (cost.Plan, error) {
	rows, err := e.db.QueryContext(ctx, "EXPLAIN (FORMAT JSON) "+query, args...)
	if err != nil {
		return cost.Plan{}, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return cost.Plan{ScanType: cost.ScanUnknown}, nil
	}
	var raw string
	if err := rows.Scan(&raw); err != nil {
		return cost.Plan{ScanType: cost.ScanUnknown}, nil
	}
	p, root, err := parsePGExplain([]byte(raw))
	if err != nil {
		return p, err
	}
	if relation := relationForScan(root, p.ScanType); relation != "" {
		p.StatsFresh = e.statsFresh(ctx, relation)
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
	if err := e.db.QueryRowContext(ctx, tableRowsSQL, relations).Scan(&found, &rows); err != nil ||
		found != int64(len(relations)) {
		return 0, false
	}
	return rows, true
}

// statsFresh reports whether the relation has been analyzed at least once.
func (e pgExplainer) statsFresh(ctx context.Context, rel string) bool {
	var last sql.NullTime
	err := e.db.QueryRowContext(ctx,
		`SELECT last_analyze FROM pg_stat_user_tables WHERE relname=$1 LIMIT 1`, rel).Scan(&last)
	if err != nil {
		// Probe failure (including sql.ErrNoRows for a table absent from the
		// stats view): be permissive rather than blocking queries because the
		// stats view is inaccessible or the table was never analyzed-tracked.
		return true
	}
	return last.Valid
}

type pgPlanNode struct {
	NodeType        string       `json:"Node Type"`
	RelationName    string       `json:"Relation Name"`
	TotalCost       float64      `json:"Total Cost"`
	PlanRows        int64        `json:"Plan Rows"`
	ActualRows      int64        `json:"Actual Rows"`
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
	p.ActualRows = actualRowsProcessed(arr[0].Plan)
	p.ExecutionTime = time.Duration(arr[0].ExecutionTime * float64(time.Millisecond))
	return p, nil
}

func actualRowsProcessed(node pgPlanNode) int64 {
	rows := node.ActualRows * node.ActualLoops
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
	case "Sort":
		p.HasSort = true
	case "Materialize", "Hash", "Hash Join", "Merge Join":
		p.HasTempTable = true
	}
	if n.SubplansRemoved > 0 {
		p.PartitionPruned = true
	}
	for _, sub := range n.Plans {
		walkPlan(sub, p)
	}
}

func relationForScan(node pgPlanNode, scan cost.ScanType) string {
	if pgScanType(node.NodeType) == scan && node.RelationName != "" {
		return node.RelationName
	}
	for _, child := range node.Plans {
		if relation := relationForScan(child, scan); relation != "" {
			return relation
		}
	}
	return ""
}

func pgScanType(node string) cost.ScanType {
	switch {
	case strings.Contains(node, "Seq Scan"):
		// PostgreSQL Seq Scan reads the whole heap: treat as a full scan so
		// RejectFullScan catches unfiltered queries.
		return cost.ScanFull
	case strings.Contains(node, "Index Only Scan"):
		return cost.ScanIndex
	case strings.Contains(node, "Index Scan"):
		return cost.ScanIndex
	case strings.Contains(node, "Tid Scan"):
		return cost.ScanPoint
	}
	return cost.ScanUnknown
}
