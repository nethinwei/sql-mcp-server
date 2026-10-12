// Package all registers every database provider built into the server binary.
//
//go:generate go run ../../../internal/capgen -root ../../..
package all

import (
	_ "github.com/nethinwei/sql-mcp-server/x/providers/mysql"
	_ "github.com/nethinwei/sql-mcp-server/x/providers/oceanbase"
	_ "github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)
