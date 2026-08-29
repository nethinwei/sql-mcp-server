//go:build integration

package hologres_test

import (
	"context"
	"testing"

	workload "github.com/nethinwei/sql-mcp-server/fixtures/v4/generator"
	"github.com/nethinwei/sql-mcp-server/internal/conformance"
	hgprov "github.com/nethinwei/sql-mcp-server/x/providers/hologres"
)

// TestHologresWorkloadConformance checks the fixtures/v4 business workload
// differentially on Hologres: the generator renders the same plain DDL as for
// PostgreSQL, so checksum equality proves the governed queries see the same
// logical data as MySQL/OceanBase/PostgreSQL deployments.
func TestHologresWorkloadConformance(t *testing.T) {
	prov, cleanup := setupHG(t)
	defer cleanup()
	d := hgprov.Dialect{}
	cfg := workload.DefaultConfig()
	if err := conformance.SetupWorkload(context.Background(), prov, d, cfg, itSchema); err != nil {
		t.Fatal(err)
	}
	conformance.RunWorkload(t, prov, d, cfg, itSchema)
}
