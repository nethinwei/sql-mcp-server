//go:build integration

package oceanbase_test

import (
	"context"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/internal/conformance"
	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
	"github.com/nethinwei/sql-mcp-server/x/providers/oceanbase"
)

// TestOBConformance runs the differential conformance suite: reference
// interpreter (docs/design/ir-semantics.md) vs OceanBase through codegen,
// then the cross-provider governance tasks.
// The DSN carries no default database, so the fixture lives in a dedicated
// schema. Setup retries because a freshly bootstrapped mini instance can
// reject DDL for a while.
func TestOBConformance(t *testing.T) {
	_, dsn := startOBContainer(t)
	prov := connectOBWithRetry(t, dsn)
	defer prov.Close()
	seedOBSchema(t, prov)
	d := oceanbase.Dialect{}
	ctx := context.Background()
	var err error
	for i := 0; i < 40; i++ {
		if err = conformance.Setup(ctx, prov, d, "conformance"); err == nil {
			break
		}
		time.Sleep(5 * time.Second)
	}
	if err != nil {
		t.Fatalf("conformance setup: %v", err)
	}
	conformance.Run(t, prov, d, "conformance")
	timed, err := oceanbase.Open(dsn, providerregistry.Options{Timeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer timed.Close()
	conformance.RunGovernance(t, prov, "oceanbase", "conformance", "SELECT SLEEP(5)", timed)
}
