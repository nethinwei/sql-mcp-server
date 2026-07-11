package tool

import (
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
)

func TestRegistryEnabledFilters(t *testing.T) {
	t.Parallel()
	r, _ := NewRegistry(DefaultTools())
	enabled := r.Enabled(config.DefaultToolFlags()) // delete off
	if len(enabled) != 9 {
		t.Fatalf("got %d tools, want 9 (delete off, transaction tools on)", len(enabled))
	}
	for _, tl := range enabled {
		if tl.Info().Name == "delete_record" {
			t.Fatal("delete_record should be filtered out")
		}
	}
	all := r.Enabled(config.ToolFlags{
		DescribeEntities: true, ReadRecords: true, CreateRecord: true,
		UpdateRecord: true, DeleteRecord: true, ExecuteEntity: true, AggregateRecords: true,
	})
	if len(all) != 7 {
		t.Fatalf("got %d, want 7", len(all))
	}
}
