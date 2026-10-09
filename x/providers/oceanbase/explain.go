package oceanbase

import (
	"context"
	"database/sql"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/x/providers/mysql"
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
	plan, err := parseOBExplain([]byte(sb.String()))
	if err == nil && plan.ScanType == cost.ScanFull && !plan.TableSized {
		// The JSON plan estimates what operators output; the tables' sizes
		// are in EXPLAIN EXTENDED's optimization info.
		if text, err := e.explainText(ctx, "EXPLAIN EXTENDED "+query, args); err == nil {
			plan.TableRows, plan.TableSized = obTableRows(text)
		}
	}
	return plan, err
}

func (e obExplainer) explainText(ctx context.Context, query string, args []any) (string, error) {
	rows, err := e.db.QueryContext(ctx, query, args...)
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	var sb strings.Builder
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return "", err
		}
		sb.WriteString(s)
		sb.WriteByte('\n')
	}
	return sb.String(), rows.Err()
}

var tableRowsLine = regexp.MustCompile(`(?m)^\s*table_rows:(\d+)\s*$`)

// obTableRows reads the rows of the largest table from EXPLAIN EXTENDED's
// optimization info (one table_rows per table the plan accesses); false
// unless every full scan's table is there.
func obTableRows(text string) (int64, bool) {
	plan, info, ok := strings.Cut(text, "Optimization Info:")
	if !ok {
		return 0, false
	}
	matches := tableRowsLine.FindAllStringSubmatch(info, -1)
	scans := strings.Count(plan, "TABLE FULL SCAN")
	if scans == 0 || len(matches) < scans {
		return 0, false
	}
	var rows int64
	for _, m := range matches {
		n, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return 0, false
		}
		rows = max(rows, n)
	}
	return rows, true
}

// parseOBExplain parses OceanBase EXPLAIN FORMAT=JSON output: a tree of
// operators ({OPERATOR, NAME, EST.ROWS, EST.TIME(us), CHILD_1, ...}) whose
// table scans may sit under grouping, sorting or join operators, so the plan
// is as risky as the worst scan, and estimates the most rows any operator
// does (a join may output far more than it scans). A MySQL-compatible
// query_block is parsed as MySQL's.
func parseOBExplain(b []byte) (cost.Plan, error) {
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		return cost.Plan{ScanType: cost.ScanUnknown, Raw: b}, nil
	}
	if _, ok := root["query_block"]; ok {
		return mysql.ParseExplain(b)
	}
	if _, ok := root["OPERATOR"].(string); !ok {
		return cost.Plan{ScanType: cost.ScanUnknown, Raw: b}, nil
	}
	plan := cost.Plan{ScanType: cost.ScanUnknown, StatsFresh: true, Raw: b}
	plan.TotalCost, _ = root["EST.TIME(us)"].(float64)
	scans := 0
	var walk func(node map[string]any)
	walk = func(node map[string]any) {
		op, _ := node["OPERATOR"].(string)
		if rows, ok := node["EST.ROWS"].(float64); ok && int64(rows) > plan.EstimatedRows {
			plan.EstimatedRows = int64(rows)
		}
		switch {
		case strings.Contains(op, "SORT"):
			plan.HasSort = true
		case strings.Contains(op, "SCAN") || strings.Contains(op, "GET"):
			if scan := obScanType(op); scans == 0 || cost.Worse(scan, plan.ScanType) {
				plan.ScanType = scan
			}
			scans++
		}
		for key, child := range node {
			if c, ok := child.(map[string]any); ok && strings.HasPrefix(key, "CHILD_") {
				walk(c)
			}
		}
	}
	walk(root)
	return plan, nil
}

// obScanType maps an OceanBase operator (or MySQL access_type in the fallback
// path) to a ScanType.
func obScanType(op string) cost.ScanType {
	switch {
	case strings.Contains(op, "FULL SCAN") || strings.EqualFold(op, "ALL"):
		return cost.ScanFull
	case strings.Contains(op, "GET") || strings.EqualFold(op, "const") ||
		strings.EqualFold(op, "eq_ref") || strings.EqualFold(op, "system"):
		return cost.ScanPoint
	case strings.Contains(op, "INDEX") || strings.Contains(op, "RANGE") || strings.EqualFold(op, "ref"):
		return cost.ScanIndex
	}
	return cost.ScanUnknown
}
