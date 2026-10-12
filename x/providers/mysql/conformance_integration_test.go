//go:build integration

package mysql_test

import (
	"context"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/internal/conformance"
	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
	"github.com/nethinwei/sql-mcp-server/x/providers/mysql"
)

// TestMySQLConformance runs the differential conformance suite: reference
// interpreter (docs/design/ir-semantics.md) vs MySQL through codegen, then
// the cross-provider governance tasks.
func TestMySQLConformance(t *testing.T) {
	dsn, stop := startMySQL(t)
	defer stop()
	prov := connectMySQLWithRetry(t, dsn)
	defer prov.Close()
	d := mysql.Dialect{}
	if err := conformance.Setup(context.Background(), prov, d, ""); err != nil {
		t.Fatal(err)
	}
	conformance.Run(t, prov, d, "")
	timed, err := mysql.Open(dsn, providerregistry.Options{Timeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer timed.Close()
	conformance.RunGovernance(t, prov, "mysql", "", longRead, timed)
}
