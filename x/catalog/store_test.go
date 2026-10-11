package catalog_test

import (
	"testing"

	"github.com/nethinwei/sql-mcp-server/x/catalog"
	"github.com/nethinwei/sql-mcp-server/x/catalog/catalogtest"
)

func TestMemoryStore(t *testing.T) {
	t.Parallel()
	catalogtest.Run(t, func(*testing.T) catalog.Store { return catalog.NewMemoryStore() })
}
