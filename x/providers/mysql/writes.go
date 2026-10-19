package mysql

import (
	"context"
	"database/sql"
	"regexp"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/x/providers/sqladapter"
)

// readingPrivileges are the privileges that change nothing; any other one
// (including those not known here) may write.
var readingPrivileges = map[string]bool{
	"usage": true, "select": true, "show view": true, "show databases": true, "process": true,
	"replication client": true, "replication slave": true, "lock tables": true, "show_routine": true,
}

// grantLine is one line of SHOW GRANTS: what it grants, and whether it
// grants on objects (ON ...) rather than roles.
var grantLine = regexp.MustCompile(`(?is)^GRANT\s+(.*?)\s+ON\s+`)

// columnLists are the column lists of column-level grants, SELECT (`a`, `b`).
var columnLists = regexp.MustCompile(`\([^)]*\)`)

// CanWrite implements introspect.WriteInspector from SHOW GRANTS, which,
// unlike information_schema, also lists routine-level grants and granted
// roles. A granted role, or a line it cannot read, leaves it unknown.
func (i Introspector) CanWrite(ctx context.Context) (introspect.Privilege, error) {
	var lines []string
	err := sqladapter.Each(ctx, i.db, "SHOW GRANTS FOR CURRENT_USER()", nil, func(rows *sql.Rows) error {
		var line string
		if err := rows.Scan(&line); err != nil {
			return err
		}
		lines = append(lines, line)
		return nil
	})
	if err != nil {
		return introspect.PrivilegeUnknown, err
	}
	return grantsWrite(lines), nil
}

// grantsWrite judges SHOW GRANTS lines (see CanWrite).
func grantsWrite(lines []string) introspect.Privilege {
	for _, line := range lines {
		m := grantLine.FindStringSubmatch(line)
		if m == nil {
			return introspect.PrivilegeUnknown // a role, or a form not known here
		}
		for _, p := range strings.Split(columnLists.ReplaceAllString(m[1], ""), ",") {
			if !readingPrivileges[strings.ToLower(strings.TrimSpace(p))] {
				return introspect.PrivilegeGranted
			}
		}
	}
	if len(lines) == 0 {
		return introspect.PrivilegeUnknown
	}
	return introspect.PrivilegeDenied
}
