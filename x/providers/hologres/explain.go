package hologres

import (
	"context"
	"database/sql"
	"regexp"
	"strconv"

	"github.com/nethinwei/sql-mcp-server/core/cost"
)

// hgExplainer estimates a plan from Hologres' text EXPLAIN output. Hologres
// documents no JSON plan format, so this parser is deliberately conservative:
// it recognizes PostgreSQL-shaped node names and row estimates and degrades to
// ScanUnknown on anything unexpected. The dialect declares ExplainCost=false,
// so the gate never assembles an Estimate layer from this; it exists for
// diagnostics and for a future, evidence-backed upgrade.
type hgExplainer struct {
	db *sql.DB
}

// Explain implements cost.Explainer. Text plans arrive one row per line and
// are concatenated before parsing (Hologres mirrors PostgreSQL here).
func (e hgExplainer) Explain(ctx context.Context, query string, args []any) (cost.Plan, error) {
	rows, err := e.db.QueryContext(ctx, "EXPLAIN "+query, args...)
	if err != nil {
		return cost.Plan{}, err
	}
	defer func() { _ = rows.Close() }()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			break
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		return cost.Plan{}, err
	}
	return parseHologresExplain(lines), nil
}

var (
	estimateRows = regexp.MustCompile(`rows=(\d+)`)
	sortNode     = regexp.MustCompile(`\bSort\b`)
	hashNode     = regexp.MustCompile(`\bHash Join\b|\bHash\b|\bMaterialize\b`)
)

// parseHologresExplain parses text EXPLAIN output, degrading on surprise.
func parseHologresExplain(lines []string) cost.Plan {
	p := cost.Plan{ScanType: cost.ScanUnknown}
	for _, line := range lines {
		// Retain the riskiest concrete scan across the whole plan, mirroring
		// the JSON plan-tree walk in the postgres explainer.
		switch {
		case containsNode(line, "Seq Scan"):
			p.ScanType = scanRiskier(p.ScanType, cost.ScanFull)
		case containsNode(line, "Index Only Scan"), containsNode(line, "Index Scan"):
			p.ScanType = scanRiskier(p.ScanType, cost.ScanIndex)
		case containsNode(line, "Tid Scan"):
			p.ScanType = scanRiskier(p.ScanType, cost.ScanPoint)
		}
		for _, m := range estimateRows.FindAllStringSubmatch(line, -1) {
			if n, err := strconv.ParseInt(m[1], 10, 64); err == nil && n > p.EstimatedRows {
				p.EstimatedRows = n
			}
		}
		if sortNode.MatchString(line) {
			p.HasSort = true
		}
		if hashNode.MatchString(line) {
			p.HasTempTable = true
		}
	}
	return p
}

// containsNode reports whether a plan line introduces the given node type,
// tolerating the leading indentation and arrow prefixes of tree output.
func containsNode(line, node string) bool {
	rest := line
	for i := 0; i < len(rest)-len(node)+1; i++ {
		if rest[i:i+len(node)] == node {
			before := byte(' ')
			if i > 0 {
				before = rest[i-1]
			}
			after := byte(' ')
			if i+len(node) < len(rest) {
				after = rest[i+len(node)]
			}
			if !isIdentChar(before) && !isIdentChar(after) {
				return true
			}
		}
	}
	return false
}

func isIdentChar(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func scanRiskier(current, candidate cost.ScanType) cost.ScanType {
	if risk(candidate) > risk(current) {
		return candidate
	}
	return current
}

func risk(scan cost.ScanType) int {
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
