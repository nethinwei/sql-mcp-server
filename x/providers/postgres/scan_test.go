package postgres

import (
	"slices"
	"strings"
	"testing"
)

// A catalog query that groups rows groups by every column it orders by.
// PostgreSQL 14 and later take the other columns of a catalog row as
// determined by its oid (a primary key); before 14, and on the servers of its
// lineage (Hologres), the scan fails with "must appear in the GROUP BY
// clause" (42803).
func TestCatalogQueriesGroupByWhatTheyOrderBy(t *testing.T) {
	t.Parallel()
	for name, query := range map[string]string{"indexesSQL": indexesSQL, "cascadesSQL": cascadesSQL} {
		group, order := clauseItems(query, "GROUP BY", "ORDER BY"), clauseItems(query, "ORDER BY", "")
		if len(group) == 0 || len(order) == 0 {
			t.Fatalf("%s: GROUP BY %v, ORDER BY %v", name, group, order)
		}
		for _, item := range order {
			column := strings.TrimSuffix(strings.TrimSuffix(item, " DESC"), " ASC")
			if !slices.Contains(group, column) {
				t.Errorf("%s orders by %s, which it does not group by", name, column)
			}
		}
	}
}

// clauseItems are the comma-separated items of the query's last clause
// beginning with keyword, up to end (or the end of the query).
func clauseItems(query, keyword, end string) []string {
	i := strings.LastIndex(query, keyword)
	if i < 0 {
		return nil
	}
	clause := query[i+len(keyword):]
	if j := strings.Index(clause, end); end != "" && j >= 0 {
		clause = clause[:j]
	}
	var out []string
	for _, item := range strings.Split(clause, ",") {
		out = append(out, strings.Join(strings.Fields(item), " "))
	}
	return out
}
