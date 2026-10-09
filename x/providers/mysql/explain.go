package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/cost"
)

// mysqlExplainer estimates a plan via EXPLAIN FORMAT=JSON.
type mysqlExplainer struct {
	db *sql.DB
}

// Explain implements cost.Explainer. MySQL estimates are less trustworthy, so
// the gate weakens this layer (see NewGateFromCapabilities). Parse failures
// degrade to ScanUnknown.
func (e mysqlExplainer) Explain(ctx context.Context, query string, args []any) (cost.Plan, error) {
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
	return ParseExplain([]byte(raw))
}

// ParseExplain parses MySQL EXPLAIN FORMAT=JSON output (OceanBase may mirror
// it), degrading on
// surprise. The tables a query reads may sit under grouping, ordering or
// join steps, so the plan is as risky as the worst access among them, and
// estimates the most rows any table is scanned for or a join produces; a sort
// or temporary table at any step counts.
func ParseExplain(b []byte) (cost.Plan, error) {
	var root struct {
		QueryBlock map[string]any `json:"query_block"`
	}
	if err := json.Unmarshal(b, &root); err != nil || root.QueryBlock == nil {
		return cost.Plan{ScanType: cost.ScanUnknown, Raw: b}, nil
	}
	plan := cost.Plan{ScanType: cost.ScanUnknown, StatsFresh: true, Raw: b}
	if info, ok := root.QueryBlock["cost_info"].(map[string]any); ok {
		total, _ := info["query_cost"].(string)
		plan.TotalCost, _ = strconv.ParseFloat(strings.TrimSpace(total), 64)
	}
	tables := 0
	eachJSONObject(root.QueryBlock, func(node map[string]any) {
		if sort, _ := node["using_filesort"].(bool); sort {
			plan.HasSort = true
		}
		if temp, _ := node["using_temporary_table"].(bool); temp {
			plan.HasTempTable = true
		}
		access, ok := node["access_type"].(string)
		if !ok {
			return
		}
		if scan := mysqlScanType(access); tables == 0 || cost.Worse(scan, plan.ScanType) {
			plan.ScanType = scan
		}
		tables++
		for _, key := range []string{"rows_examined_per_scan", "rows_produced_per_join"} {
			if rows, ok := node[key].(float64); ok && int64(rows) > plan.EstimatedRows {
				plan.EstimatedRows = int64(rows)
			}
		}
	})
	return plan, nil
}

// eachJSONObject calls fn for v and every object nested in it.
func eachJSONObject(v any, fn func(map[string]any)) {
	switch v := v.(type) {
	case map[string]any:
		fn(v)
		for _, child := range v {
			eachJSONObject(child, fn)
		}
	case []any:
		for _, child := range v {
			eachJSONObject(child, fn)
		}
	}
}

func mysqlScanType(at string) cost.ScanType {
	switch at {
	case "ALL":
		return cost.ScanFull
	case "index", "ref", "range", "index_merge", "fulltext":
		return cost.ScanIndex
	case "const", "eq_ref", "system", "unique_subquery":
		return cost.ScanPoint
	}
	return cost.ScanUnknown
}
