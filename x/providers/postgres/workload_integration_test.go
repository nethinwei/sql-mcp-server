//go:build integration

package postgres_test

import (
	"context"
	"testing"

	workload "github.com/nethinwei/sql-mcp-server/fixtures/v4/generator"
	"github.com/nethinwei/sql-mcp-server/internal/conformance"
	pgprov "github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

// TestPGWorkloadConformance checks the fixtures/v4 business workload
// differentially on PostgreSQL: per-table checksum aggregates evaluated by
// the reference interpreter vs the provider through codegen (design doc
// acceptance #6, docs/design/business-workload-model.md).
func TestPGWorkloadConformance(t *testing.T) {
	prov, cleanup := setupPG(t)
	defer cleanup()
	d := pgprov.Dialect{}
	cfg := workload.DefaultConfig()
	if err := conformance.SetupWorkload(context.Background(), prov, d, cfg, ""); err != nil {
		t.Fatal(err)
	}
	conformance.RunWorkload(t, prov, d, cfg, "")
}
