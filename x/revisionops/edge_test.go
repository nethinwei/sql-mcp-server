package revisionops_test

import (
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/revisionops/revisionopstest"
)

func TestEdgeMemoryStore(t *testing.T) {
	t.Parallel()
	revisionopstest.Run(t, func(*testing.T) revision.Store { return revision.NewMemoryStore(nil) },
		revisionopstest.Options{Admins: 16, Rounds: 8})
}
