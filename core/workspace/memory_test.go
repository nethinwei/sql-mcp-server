package workspace_test

import (
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/workspace"
	"github.com/nethinwei/sql-mcp-server/core/workspace/workspacetest"
)

func TestMemoryStoreConformance(t *testing.T) {
	workspacetest.Run(t, func(*testing.T) workspace.Store { return workspace.NewMemoryStore(nil) })
}
