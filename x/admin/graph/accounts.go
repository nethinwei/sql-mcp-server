package graph

import (
	"github.com/nethinwei/sql-mcp-server/x/admin/accounts"
)

func toAdminAccount(a accounts.Account) *AdminAccount {
	return &AdminAccount{
		Username: a.Username, Permissions: orEmpty(a.Permissions), Disabled: a.Disabled,
		CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
	}
}
