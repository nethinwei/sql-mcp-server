package oceanbase

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/nethinwei/sql-mcp-server/cost"
)

// obExplainer estimates a plan via EXPLAIN FORMAT=JSON.
type obExplainer struct {
	db *sql.DB
}

// Explain implements cost.Explainer. OceanBase plan JSON varies by version; we
// try MySQL-style query_block first, then OceanBase op/cost/rows shapes,
// degrading to ScanUnknown on any mismatch (never panic).
func (e obExplainer) Explain(ctx context.Context, query string, args []any) (cost.Plan, error) {
	rows, err := e.db.QueryContext(ctx, "EXPLAIN FORMAT=JSON "+query, args...)
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
	return parseOBExplain([]byte(raw))
}

// parseOBExplain parses OceanBase EXPLAIN JSON, trying several shapes.
func parseOBExplain(b []byte) (cost.Plan, error) {
	// Try MySQL-compatible shape (OceanBase MySQL mode often mirrors it).
	var ex struct {
		QueryBlock struct {
			CostInfo struct {
				QueryCost string `json:"query_cost"`
			} `json:"cost_info"`
			Table struct {
				AccessType string `json:"access_type"`
				Rows       int64  `json:"rows"`
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

	// Try OceanBase op/cost/rows shapes: an object or an array of them.
	if plan := parseOBNode(b); plan != nil {
		return *plan, nil
	}
	return cost.Plan{ScanType: cost.ScanUnknown, Raw: b}, nil
}

// parseOBNode looks for a node with op/cost/rows in object or array form.
func parseOBNode(b []byte) *cost.Plan {
	var single struct {
		Op   string  `json:"op"`
		Cost float64 `json:"cost"`
		Rows int64   `json:"rows"`
	}
	if err := json.Unmarshal(b, &single); err == nil && single.Op != "" {
		return &cost.Plan{TotalCost: single.Cost, EstimatedRows: single.Rows, ScanType: obScanType(single.Op), StatsFresh: true, Raw: b}
	}
	var arr []struct {
		Op   string  `json:"op"`
		Cost float64 `json:"cost"`
		Rows int64   `json:"rows"`
	}
	if err := json.Unmarshal(b, &arr); err == nil && len(arr) > 0 {
		n := arr[0]
		return &cost.Plan{TotalCost: n.Cost, EstimatedRows: n.Rows, ScanType: obScanType(n.Op), StatsFresh: true, Raw: b}
	}
	return nil
}

// obScanType maps an OceanBase operator/access string to a ScanType.
func obScanType(op string) cost.ScanType {
	switch {
	case strings.Contains(op, "FULL") || strings.Contains(op, "TABLE SCAN"):
		return cost.ScanFull
	case strings.Contains(op, "POINT GET") || strings.Contains(op, "PK SCAN"):
		return cost.ScanPoint
	case strings.Contains(op, "INDEX"):
		return cost.ScanIndex
	case strings.EqualFold(op, "const") || strings.EqualFold(op, "eq_ref") || strings.EqualFold(op, "system"):
		return cost.ScanPoint
	case strings.EqualFold(op, "ALL"):
		return cost.ScanFull
	}
	return cost.ScanUnknown
}
