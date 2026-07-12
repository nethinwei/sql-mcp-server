//go:build integration

package mysql_test

import (
	"context"
	"testing"

	"github.com/nethinwei/sql-mcp-server/internal/conformance"
	"github.com/nethinwei/sql-mcp-server/x/providers/mysql"
)

// TestMySQLConformance runs the differential conformance suite: reference
// interpreter (docs/design/ir-semantics.md) vs MySQL through codegen.
func TestMySQLConformance(t *testing.T) {
	prov, cleanup := setupMySQL(t)
	defer cleanup()
	d := mysql.Dialect{}
	if err := conformance.Setup(context.Background(), prov, d, ""); err != nil {
		t.Fatal(err)
	}
	conformance.Run(t, prov, d, "")
}
