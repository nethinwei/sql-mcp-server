package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
	if scanned := scannedRelations(root, nil); len(scanned) > 0 {
		p.StatsFresh = e.statsFresh(ctx, scanned)
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
func fullScanRelations(n pgPlanNode, out []relation) ([]relation, bool) {
	if pgScanType(n.NodeType) == cost.ScanFull {
		if n.RelationName == "" {
			return nil, false
		}
		out = append(out, relation{schema: n.Schema, name: n.RelationName})
	}
	for _, child := range n.Plans {
		var ok bool
		if out, ok = fullScanRelations(child, out); !ok {
			return nil, false
		}
	}
	return out, true
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
	tx, err := beginner.BeginTx(ctx, &store.TxOptions{Isolation: store.LevelDefault, ReadOnly: true})
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

// scannedRelations lists the tables the plan's scans read, whatever the
// scan: the planner's statistics of each count.
func scannedRelations(n pgPlanNode, out []relation) []relation {
	if n.RelationName != "" && pgScanType(n.NodeType) != cost.ScanUnknown {
		out = append(out, relation{schema: n.Schema, name: n.RelationName})
	}
	for _, child := range n.Plans {
		out = scannedRelations(child, out)
	}
	return out
}

func pgScanType(node string) cost.ScanType {
	switch {
	case strings.Contains(node, "Seq Scan"):
		// PostgreSQL Seq Scan reads the whole heap: treat as a full scan so
		// RejectFullScan catches unfiltered queries.
		return cost.ScanFull
	case strings.HasSuffix(node, "Bitmap Heap Scan"): // "Parallel Bitmap Heap Scan" too
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
