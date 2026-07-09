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
// the JSON plan. Any parse failure degrades to ScanUnknown rather than erroring,
// so a malformed plan never panics or blocks the gate.
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
	return parsePGExplain([]byte(raw))
}

type pgPlanNode struct {
	NodeType  string  `json:"Node Type"`
	TotalCost float64 `json:"Total Cost"`
	PlanRows  int64   `json:"Plan Rows"`
}

type pgExplainRow struct {
	Plan pgPlanNode `json:"Plan"`
}

// parsePGExplain parses EXPLAIN (FORMAT JSON) output. On any structural
// surprise it returns ScanUnknown (Murphy: plan drift must not panic).
func parsePGExplain(b []byte) (cost.Plan, error) {
	var arr []pgExplainRow
	if err := json.Unmarshal(b, &arr); err != nil {
		return cost.Plan{ScanType: cost.ScanUnknown, Raw: b}, nil
	}
	if len(arr) == 0 {
		return cost.Plan{ScanType: cost.ScanUnknown, Raw: b}, nil
	}
	p := arr[0].Plan
	return cost.Plan{
		TotalCost:     p.TotalCost,
		EstimatedRows: p.PlanRows,
		ScanType:      pgScanType(p.NodeType),
		StatsFresh:    true,
		Raw:           b,
	}, nil
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
