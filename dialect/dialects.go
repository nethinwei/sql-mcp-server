package dialect

import "strings"

// Postgres is the PostgreSQL dialect.
type Postgres struct{}

func (Postgres) Name() string { return "postgres" }
func (Postgres) QuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
func (Postgres) Placeholder(i int) string   { return "$" + itoa(i+1) }
func (Postgres) ExplainSQL(q string) string { return "EXPLAIN (FORMAT JSON) " + q }
func (Postgres) Capabilities() Capabilities {
	return Capabilities{
		Returning:        true,
		Savepoint:        true,
		KeysetCursor:     true,
		ExplainJSON:      true,
		ExplainCost:      true,
		ExplainAccurate:  true,
		StatementTimeout: true,
		ScanRowCap:       false,
		SQLSafeUpdates:   false,
		ResourceManager:  false,
	}
}

// MySQL is the MySQL dialect. EXPLAIN estimates are less reliable than
// PostgreSQL's, so the gate weakens the Estimate layer and leans on
// EnforceCap + max_execution_time + sql_safe_updates.
type MySQL struct{}

func (MySQL) Name() string                  { return "mysql" }
func (MySQL) QuoteIdent(name string) string { return "`" + strings.ReplaceAll(name, "`", "``") + "`" }
func (MySQL) Placeholder(_ int) string      { return "?" }
func (MySQL) ExplainSQL(q string) string    { return "EXPLAIN FORMAT=JSON " + q }
func (MySQL) Capabilities() Capabilities {
	return Capabilities{
		Returning:        false,
		Savepoint:        true,
		KeysetCursor:     true,
		ExplainJSON:      true,
		ExplainCost:      true,
		ExplainAccurate:  false,
		StatementTimeout: true,
		ScanRowCap:       true,
		SQLSafeUpdates:   true,
		ResourceManager:  false,
	}
}

// OceanBase speaks the MySQL wire protocol and reuses its quoting/placeholder
// rules. Distributed plans make estimates harder to trust, so the gate relies
// on ob_query_timeout + max_read_size (a runtime scan-row hard cap independent
// of estimates) + tenant resource isolation.
type OceanBase struct{}

func (OceanBase) Name() string                  { return "oceanbase" }
func (OceanBase) QuoteIdent(name string) string { return MySQL{}.QuoteIdent(name) }
func (OceanBase) Placeholder(i int) string      { return MySQL{}.Placeholder(i) }
func (OceanBase) ExplainSQL(q string) string    { return MySQL{}.ExplainSQL(q) }
func (OceanBase) Capabilities() Capabilities {
	c := MySQL{}.Capabilities()
	c.ExplainAccurate = false
	c.ResourceManager = true
	return c
}

// itoa converts a non-negative int to its decimal string without strconv, which
// is overkill for placeholder indexes.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
