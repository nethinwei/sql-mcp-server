// Package all registers every database provider built into the server binary.
package all

import (
	_ "github.com/nethinwei/sql-mcp-server/x/providers/mysql"
	_ "github.com/nethinwei/sql-mcp-server/x/providers/oceanbase"
	_ "github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)
