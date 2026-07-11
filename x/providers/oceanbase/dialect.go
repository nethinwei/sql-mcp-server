package oceanbase

import (
	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/x/providers/mysql"
)

// Dialect is the OceanBase dialect. OceanBase speaks the MySQL wire protocol and
// reuses its quoting/placeholder rules. Distributed plans make estimates harder
// to trust, so the gate relies on ob_query_timeout + max_read_size (a runtime
// scan-row hard cap independent of estimates) + tenant resource isolation.
type Dialect struct{}

func (Dialect) Name() string                  { return "oceanbase" }
func (Dialect) QuoteIdent(name string) string { return mysql.Dialect{}.QuoteIdent(name) }
func (Dialect) Placeholder(i int) string      { return mysql.Dialect{}.Placeholder(i) }
func (Dialect) ExplainSQL(q string) string    { return mysql.Dialect{}.ExplainSQL(q) }
func (Dialect) Capabilities() dialect.Capabilities {
	c := mysql.Dialect{}.Capabilities()
	c.ExplainAccurate = false
	c.ResourceManager = true
	return c
}
