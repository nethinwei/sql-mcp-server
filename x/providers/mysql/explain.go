package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/nethinwei/sql-mcp-server/cost"
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
	return parseMySQLExplain([]byte(raw))
}

type mysqlExplain struct {
	QueryBlock struct {
		CostInfo struct {
			QueryCost string `json:"query_cost"`
		} `json:"cost_info"`
		Table struct {
			AccessType     string `json:"access_type"`
			Rows           int64  `json:"rows"`
			UsingFilesort  bool   `json:"using_filesort"`
			UsingTempTable bool   `json:"using_temporary_table"`
		} `json:"table"`
	} `json:"query_block"`
}

// parseMySQLExplain parses EXPLAIN FORMAT=JSON output, degrading on surprise.
func parseMySQLExplain(b []byte) (cost.Plan, error) {
	var ex mysqlExplain
	if err := json.Unmarshal(b, &ex); err != nil {
		return cost.Plan{ScanType: cost.ScanUnknown, Raw: b}, nil
	}
	qb := ex.QueryBlock
	c, _ := strconv.ParseFloat(strings.TrimSpace(qb.CostInfo.QueryCost), 64)
	return cost.Plan{
		TotalCost:     c,
		EstimatedRows: qb.Table.Rows,
		ScanType:      mysqlScanType(qb.Table.AccessType),
		HasSort:       qb.Table.UsingFilesort,
		HasTempTable:  qb.Table.UsingTempTable,
		StatsFresh:    true,
		Raw:           b,
	}, nil
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
