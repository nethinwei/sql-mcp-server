package graph

import (
	"context"
	"errors"
	"slices"

	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/configstore"
)

// ErrLastAccountManager prevents removing the last enabled administrator
// that can manage accounts.
var ErrLastAccountManager = errors.New("admin: at least one enabled account must keep admin:accounts")

func toAdminAccount(a configstore.AdminAccount) *AdminAccount {
	return &AdminAccount{
		Username: a.Username, Permissions: orEmpty(a.Permissions), Disabled: a.Disabled,
		CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
	}
}

func managesAccounts(a configstore.AdminAccount) bool {
	return !a.Disabled && (slices.Contains(a.Permissions, auth.PermAccounts) ||
		slices.Contains(a.Permissions, auth.PermAll))
}

// checkAccountManagers fails when replacing changed would leave no enabled
// account with admin:accounts.
func (r *Resolver) checkAccountManagers(ctx context.Context, changed configstore.AdminAccount) error {
	all, err := r.Accounts.ListAdmins(ctx)
	if err != nil {
		return err
	}
	for _, a := range all {
		if a.Username == changed.Username {
			a = changed
		}
		if managesAccounts(a) {
			return nil
		}
	}
	return ErrLastAccountManager
}
