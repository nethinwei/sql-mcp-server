package mysql

import (
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/introspect"
)

func TestGrantsWrite(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		lines []string
		want  introspect.Privilege
	}{
		"reads only": {[]string{
			"GRANT USAGE ON *.* TO `ro`@`%`",
			"GRANT SELECT, SHOW VIEW ON `shop`.* TO `ro`@`%`",
			"GRANT SELECT (`id`, `email`) ON `shop`.`users` TO `ro`@`%`",
		}, introspect.PrivilegeDenied},
		"data writes": {[]string{"GRANT SELECT, INSERT ON `shop`.* TO `rw`@`%`"}, introspect.PrivilegeGranted},
		"column update": {[]string{"GRANT UPDATE (`email`) ON `shop`.`users` TO `u`@`%`"},
			introspect.PrivilegeGranted},
		"a routine": {[]string{"GRANT EXECUTE ON PROCEDURE `shop`.`refresh` TO `u`@`%`"},
			introspect.PrivilegeGranted},
		"all": {[]string{"GRANT ALL PRIVILEGES ON *.* TO `root`@`%` WITH GRANT OPTION"},
			introspect.PrivilegeGranted},
		"dynamic privilege": {[]string{"GRANT BACKUP_ADMIN ON *.* TO `u`@`%`"}, introspect.PrivilegeGranted},
		"a granted role": {[]string{"GRANT USAGE ON *.* TO `u`@`%`", "GRANT `writer`@`%` TO `u`@`%`"},
			introspect.PrivilegeUnknown},
		"nothing listed": {nil, introspect.PrivilegeUnknown},
	} {
		if got := grantsWrite(c.lines); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}
