//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/internal/conformance"
	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
	pgprov "github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

// TestPGConformance runs the differential conformance suite: reference
// interpreter (docs/design/ir-semantics.md) vs PostgreSQL through codegen,
// then the cross-provider governance tasks.
func TestPGConformance(t *testing.T) {
	dsn, stop := startPG(t)
	defer stop()
	prov, err := pgprov.New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer prov.Close()
	d := pgprov.Dialect{}
	if err := conformance.Setup(context.Background(), prov, d, ""); err != nil {
		t.Fatal(err)
	}
	conformance.Run(t, prov, d, "")
	timed, err := pgprov.Open(dsn, providerregistry.Options{Timeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer timed.Close()
	conformance.RunGovernance(t, prov, "postgres", "", "SELECT pg_sleep(5)", timed)
}
