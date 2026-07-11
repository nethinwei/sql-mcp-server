package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nethinwei/sql-mcp-server/codegen"
	"github.com/nethinwei/sql-mcp-server/cost"
	"github.com/nethinwei/sql-mcp-server/store"
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
	return p, nil
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
		return cost.Plan{ScanType: cost.ScanUnknown, Raw: b}, fmt.Errorf("postgres: parse EXPLAIN ANALYZE JSON: %w", err)
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
		(p.ScanType == cost.ScanUnknown || scanRisk(scan) > scanRisk(p.ScanType)) {
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

func scanRisk(scan cost.ScanType) int {
	switch scan {
	case cost.ScanFull:
		return 4
	case cost.ScanSeq:
		return 3
	case cost.ScanIndex:
		return 2
	case cost.ScanPoint:
		return 1
	default:
		return 0
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
