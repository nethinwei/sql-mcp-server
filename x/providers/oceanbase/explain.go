package oceanbase

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/cost"
)

// obExplainer estimates a plan via EXPLAIN FORMAT=JSON.
type obExplainer struct {
	db *sql.DB
}

// Explain implements cost.Explainer. OceanBase plan JSON varies by version; we
// parse the real OceanBase shape (OPERATOR/EST.ROWS/EST.TIME) first, fall back
// to a MySQL-compatible query_block, and degrade to ScanUnknown on any
// mismatch (never panic).
func (e obExplainer) Explain(ctx context.Context, query string, args []any) (cost.Plan, error) {
	rows, err := e.db.QueryContext(ctx, "EXPLAIN FORMAT=JSON "+query, args...)
	if err != nil {
		return cost.Plan{}, err
	}
	defer func() { _ = rows.Close() }()
	// OceanBase returns the JSON plan across multiple rows; concatenate them.
	var sb strings.Builder
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return cost.Plan{ScanType: cost.ScanUnknown}, nil
		}
		sb.WriteString(s)
	}
	if err := rows.Err(); err != nil {
		return cost.Plan{}, err
	}
	if sb.Len() == 0 {
		return cost.Plan{ScanType: cost.ScanUnknown}, nil
	}
	return parseOBExplain([]byte(sb.String()))
}

// parseOBExplain parses OceanBase EXPLAIN FORMAT=JSON output.
func parseOBExplain(b []byte) (cost.Plan, error) {
	// Real OceanBase shape: {OPERATOR, NAME, EST.ROWS, EST.TIME(us), ...}.
	var ob struct {
		Operator string  `json:"OPERATOR"`
		Name     string  `json:"NAME"`
		EstRows  int64   `json:"EST.ROWS"`
		EstTime  float64 `json:"EST.TIME(us)"`
	}
	if err := json.Unmarshal(b, &ob); err == nil && ob.Operator != "" {
		return cost.Plan{
			TotalCost:     ob.EstTime,
			EstimatedRows: ob.EstRows,
			ScanType:      obScanType(ob.Operator),
			StatsFresh:    true,
			Raw:           b,
		}, nil
	}
	// Fallback: MySQL-compatible query_block (OceanBase MySQL mode may mirror it).
	var ex struct {
		QueryBlock struct {
			CostInfo struct {
				QueryCost string `json:"query_cost"`
			} `json:"cost_info"`
			Table struct {
				AccessType string `json:"access_type"`
				Rows       int64  `json:"rows_examined_per_scan"`
			} `json:"table"`
		} `json:"query_block"`
	}
	if err := json.Unmarshal(b, &ex); err == nil && ex.QueryBlock.Table.AccessType != "" {
		c, _ := strconv.ParseFloat(strings.TrimSpace(ex.QueryBlock.CostInfo.QueryCost), 64)
		return cost.Plan{
			TotalCost:     c,
			EstimatedRows: ex.QueryBlock.Table.Rows,
			ScanType:      obScanType(ex.QueryBlock.Table.AccessType),
			StatsFresh:    true,
			Raw:           b,
		}, nil
	}
	return cost.Plan{ScanType: cost.ScanUnknown, Raw: b}, nil
}

// obScanType maps an OceanBase operator (or MySQL access_type in the fallback
// path) to a ScanType.
func obScanType(op string) cost.ScanType {
	switch {
	case strings.Contains(op, "FULL SCAN") || strings.EqualFold(op, "ALL"):
		return cost.ScanFull
	case strings.Contains(op, "GET") || strings.EqualFold(op, "const") || strings.EqualFold(op, "eq_ref") || strings.EqualFold(op, "system"):
		return cost.ScanPoint
	case strings.Contains(op, "INDEX") || strings.Contains(op, "RANGE") || strings.EqualFold(op, "ref"):
		return cost.ScanIndex
	}
	return cost.ScanUnknown
}
