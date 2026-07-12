//go:build integration

package mysql_test

import (
	"context"
	"testing"

	workload "github.com/nethinwei/sql-mcp-server/fixtures/v4/generator"
	"github.com/nethinwei/sql-mcp-server/internal/conformance"
	"github.com/nethinwei/sql-mcp-server/x/providers/mysql"
)

// TestMySQLWorkloadConformance checks the fixtures/v4 business workload
// differentially on MySQL: per-table checksum aggregates evaluated by the
// reference interpreter vs the provider through codegen (design doc
// acceptance #6, docs/design/business-workload-model.md).
func TestMySQLWorkloadConformance(t *testing.T) {
	prov, cleanup := setupMySQL(t)
	defer cleanup()
	d := mysql.Dialect{}
	cfg := workload.DefaultConfig()
	if err := conformance.SetupWorkload(context.Background(), prov, d, cfg, ""); err != nil {
		t.Fatal(err)
	}
	conformance.RunWorkload(t, prov, d, cfg, "")
}
