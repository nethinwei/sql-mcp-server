package hologres

import (
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/cost"
)

func TestParseHologresExplain(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		lines []string
		want  cost.Plan
	}{
		{
			name: "seq scan with row estimate",
			lines: []string{
				`Seq Scan on events  (cost=0.00..15.00 rows=1000 width=32)`,
			},
			want: cost.Plan{ScanType: cost.ScanFull, EstimatedRows: 1000},
		},
		{
			name: "index scan outranks nothing, sort and hash flags set",
			lines: []string{
				`Sort  (cost=317.01..317.43 rows=170 width=8)`,
				`  ->  Hash Join  (cost=1.15..310.04 rows=170 width=8)`,
				`        ->  Index Scan using idx_events_pk on events  (cost=0.15..45.00 rows=170 width=8)`,
			},
			want: cost.Plan{ScanType: cost.ScanIndex, EstimatedRows: 170, HasSort: true, HasTempTable: true},
		},
		{
			name: "riskiest scan wins across the tree",
			lines: []string{
				`Hash Join  (cost=1.15..310.04 rows=10 width=8)`,
				`  ->  Index Scan on small  (cost=0.15..5.00 rows=10 width=4)`,
				`  ->  Seq Scan on big  (cost=0.00..200.00 rows=9000 width=4)`,
			},
			want: cost.Plan{ScanType: cost.ScanFull, EstimatedRows: 9000, HasTempTable: true},
		},
		{
			name: "node names match on word boundaries only",
			lines: []string{
				`Seq Scanner  (cost=0.00..1.00 rows=5 width=4)`,
			},
			want: cost.Plan{ScanType: cost.ScanUnknown, EstimatedRows: 5},
		},
		{
			name:  "empty output degrades to unknown",
			lines: nil,
			want:  cost.Plan{ScanType: cost.ScanUnknown},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := parseHologresExplain(c.lines)
			if got.ScanType != c.want.ScanType ||
				got.EstimatedRows != c.want.EstimatedRows ||
				got.HasSort != c.want.HasSort ||
				got.HasTempTable != c.want.HasTempTable {
				t.Fatalf("plan = %+v, want %+v", got, c.want)
			}
		})
	}
}
