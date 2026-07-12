//go:build integration

package oceanbase_test

import (
	"context"
	"testing"
	"time"

	workload "github.com/nethinwei/sql-mcp-server/fixtures/v4/generator"
	"github.com/nethinwei/sql-mcp-server/internal/conformance"
	"github.com/nethinwei/sql-mcp-server/x/providers/oceanbase"
)

// TestOBWorkloadConformance checks the fixtures/v4 business workload
// differentially on OceanBase (design doc acceptance #6). The DSN carries
// no default database, so the workload lives in a dedicated schema; setup
// retries because a freshly bootstrapped mini instance can reject DDL for
// a while.
func TestOBWorkloadConformance(t *testing.T) {
	prov, cleanup := setupOB(t)
	defer cleanup()
	d := oceanbase.Dialect{}
	cfg := workload.DefaultConfig()
	ctx := context.Background()
	var err error
	for i := 0; i < 40; i++ {
		if err = conformance.SetupWorkload(ctx, prov, d, cfg, "workload"); err == nil {
			break
		}
		time.Sleep(5 * time.Second)
	}
	if err != nil {
		t.Fatalf("workload setup: %v", err)
	}
	conformance.RunWorkload(t, prov, d, cfg, "workload")
}
