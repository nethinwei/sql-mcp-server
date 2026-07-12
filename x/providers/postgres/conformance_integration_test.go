//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/nethinwei/sql-mcp-server/internal/conformance"
	pgprov "github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

// TestPGConformance runs the differential conformance suite: reference
// interpreter (docs/design/ir-semantics.md) vs PostgreSQL through codegen.
func TestPGConformance(t *testing.T) {
	prov, cleanup := setupPG(t)
	defer cleanup()
	d := pgprov.Dialect{}
	if err := conformance.Setup(context.Background(), prov, d, ""); err != nil {
		t.Fatal(err)
	}
	conformance.Run(t, prov, d, "")
}
