//go:build integration

package hologres_test

import (
	"context"
	"testing"

	"github.com/nethinwei/sql-mcp-server/internal/conformance"
	hgprov "github.com/nethinwei/sql-mcp-server/x/providers/hologres"
)

// TestHologresConformance runs the differential conformance suite (read and
// aggregate corpus): reference interpreter vs Hologres through codegen.
func TestHologresConformance(t *testing.T) {
	prov, cleanup := setupHG(t)
	defer cleanup()
	d := hgprov.Dialect{}
	if err := conformance.Setup(context.Background(), prov, d, ""); err != nil {
		t.Fatal(err)
	}
	conformance.Run(t, prov, d, "")
}
