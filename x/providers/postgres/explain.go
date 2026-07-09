package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/nethinwei/sql-mcp-server/cost"
)

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
	if root.RelationName != "" {
		p.StatsFresh = e.statsFresh(ctx, root.RelationName)
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
	SubplansRemoved int          `json:"Subplans Removed"`
	Plans           []pgPlanNode `json:"Plans"`
}

type pgExplainRow struct {
	Plan pgPlanNode `json:"Plan"`
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

// walkPlan recurses the plan tree, setting HasSort/HasTempTable/PartitionPruned.
func walkPlan(n pgPlanNode, p *cost.Plan) {
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
