// Package accounts holds the business rules for administrator accounts. The
// admin API and the `admin` CLI both go through Service, so they share
// validation, password hashing and the rule that some enabled account can
// always manage accounts; Store.MutateAdmins makes that rule atomic.
package accounts

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/configstore"
)

// Account is one administrator.
type Account = configstore.AdminAccount

// Store persists accounts. MutateAdmins runs fn on a consistent snapshot of
// every account and writes the accounts it returns; concurrent calls are
// serialized (see configstore.SQLStore.MutateAdmins).
type Store interface {
	GetAdmin(ctx context.Context, username string) (Account, error)
	ListAdmins(ctx context.Context) ([]Account, error)
	MutateAdmins(ctx context.Context, fn func(all []Account) ([]Account, error)) ([]Account, error)
}

// ErrLastAccountManager prevents leaving no enabled account that can manage
// accounts. There is no override: `admin create` and `admin set --enable`
// never remove a manager, so they recover a store without one.
var ErrLastAccountManager = errors.New("admin: at least one enabled account must keep admin:accounts")

// Service applies the account rules on top of a Store.
type Service struct {
	Store Store
	// Gate bounds the password hashing and verification running at once;
	// nil runs them unbounded (the CLI).
	Gate Gate
}

// Gate bounds memory-hard password work (argon2id, 64 MiB each): Enter
// false means the caller must not do it; a true result is paired with
// Leave. The admin API passes the gate that bounds sign-ins.
type Gate interface {
	Enter(ctx context.Context) bool
	Leave()
}

// ErrBusy: the gate refused password work.
var ErrBusy = errors.New("admin: too many password operations in progress; try again later")

// slow runs password work through the gate, holding it for work only.
func (s Service) slow(ctx context.Context, work func() error) error {
	if s.Gate != nil {
		if !s.Gate.Enter(ctx) {
			return ErrBusy
		}
		defer s.Gate.Leave()
	}
	return work()
}

func (s Service) hash(ctx context.Context, password string) (hash string, err error) {
	err = s.slow(ctx, func() error {
		hash, err = auth.HashPassword(password)
		return err
	})
	return hash, err
}

// Patch lists the account settings to change; nil fields stay as they are.
type Patch struct {
	Permissions []string
	Disabled    *bool
}

// ManagesAccounts reports whether a is an enabled account manager.
func ManagesAccounts(a Account) bool {
	return !a.Disabled && (slices.Contains(a.Permissions, auth.PermAccounts) ||
		slices.Contains(a.Permissions, auth.PermAll))
}

func normalize(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

// Get returns one account.
func (s Service) Get(ctx context.Context, username string) (Account, error) {
	return s.Store.GetAdmin(ctx, normalize(username))
}

// List returns every account ordered by username.
func (s Service) List(ctx context.Context) ([]Account, error) {
	return s.Store.ListAdmins(ctx)
}

// Create adds an account.
func (s Service) Create(ctx context.Context, username, password string, permissions []string) (Account, error) {
	username = normalize(username)
	if err := auth.ValidateUsername(username); err != nil {
		return Account{}, err
	}
	if err := auth.ValidatePermissions(permissions); err != nil {
		return Account{}, err
	}
	if err := auth.ValidatePassword(password); err != nil {
		return Account{}, err
	}
	// Hashing is slow by design; do it before taking the store lock.
	hash, err := s.hash(ctx, password)
	if err != nil {
		return Account{}, err
	}
	created := Account{Username: username, PasswordHash: hash, Permissions: slices.Clone(permissions)}
	return s.mutate(ctx, username, func(all []Account, i int) (Account, error) {
		if i >= 0 {
			return Account{}, fmt.Errorf("%w: %q", configstore.ErrAdminExists, username)
		}
		return created, nil
	})
}

// SetPassword replaces an account's password, which ends its sessions.
func (s Service) SetPassword(ctx context.Context, username, password string) (Account, error) {
	username = normalize(username)
	if err := auth.ValidatePassword(password); err != nil {
		return Account{}, err
	}
	hash, err := s.hash(ctx, password)
	if err != nil {
		return Account{}, err
	}
	return s.mutate(ctx, username, func(all []Account, i int) (Account, error) {
		if i < 0 {
			return Account{}, fmt.Errorf("%w: %q", configstore.ErrAdminNotFound, username)
		}
		a := all[i]
		a.PasswordHash = hash
		return a, nil
	})
}

// Update changes an account's permissions or disabled flag.
func (s Service) Update(ctx context.Context, username string, p Patch) (Account, error) {
	username = normalize(username)
	if p.Permissions != nil {
		if err := auth.ValidatePermissions(p.Permissions); err != nil {
			return Account{}, err
		}
	}
	return s.mutate(ctx, username, func(all []Account, i int) (Account, error) {
		if i < 0 {
			return Account{}, fmt.Errorf("%w: %q", configstore.ErrAdminNotFound, username)
		}
		a := all[i]
		if p.Permissions != nil {
			a.Permissions = slices.Clone(p.Permissions)
		}
		if p.Disabled != nil {
			a.Disabled = *p.Disabled
		}
		return a, nil
	})
}

// ErrWrongPassword: the current password given to change one's own is not
// the account's.
var ErrWrongPassword = errors.New("admin: the current password is not correct")

// SetPasswordAs sets username's password on behalf of the signed-in account
// by. An account changing its own gives its current password, so a stolen
// session cannot lock the account's owner out; changing another account's
// needs admin:accounts, which mayManage reports, and no current password.
func (s Service) SetPasswordAs(
	ctx context.Context, by string, mayManage bool, username, current, password string,
) (Account, error) {
	username = normalize(username)
	if username != normalize(by) {
		if !mayManage {
			return Account{}, fmt.Errorf("%w: %s required to change another account's password",
				auth.ErrForbidden, auth.PermAccounts)
		}
		return s.SetPassword(ctx, username, password)
	}
	if err := auth.ValidatePassword(password); err != nil {
		return Account{}, err
	}
	own, err := s.Store.GetAdmin(ctx, username)
	if err != nil {
		return Account{}, err
	}
	var hash string
	err = s.slow(ctx, func() (err error) {
		if !auth.VerifyPassword(own.PasswordHash, current) {
			return ErrWrongPassword
		}
		hash, err = auth.HashPassword(password)
		return err
	})
	if err != nil {
		return Account{}, err
	}
	return s.mutate(ctx, username, func(all []Account, i int) (Account, error) {
		// The password verified must still be the account's.
		if i < 0 || all[i].PasswordHash != own.PasswordHash {
			return Account{}, ErrWrongPassword
		}
		a := all[i]
		a.PasswordHash = hash
		return a, nil
	})
}

// mutate changes one account (i is its index in all, or -1 when it does not
// exist) and enforces the account-manager rule on the result.
func (s Service) mutate(
	ctx context.Context, username string, change func(all []Account, i int) (Account, error),
) (Account, error) {
	written, err := s.Store.MutateAdmins(ctx, func(all []Account) ([]Account, error) {
		i := slices.IndexFunc(all, func(a Account) bool { return a.Username == username })
		next, err := change(all, i)
		if err != nil {
			return nil, err
		}
		if err := keepsManager(all, next); err != nil {
			return nil, err
		}
		return []Account{next}, nil
	})
	if err != nil {
		return Account{}, err
	}
	return written[0], nil
}

// keepsManager fails when writing next would leave accounts but no enabled
// account manager. A store that already has none (or no accounts) accepts
// any change, so recovery is never blocked.
func keepsManager(all []Account, next Account) error {
	before := slices.ContainsFunc(all, ManagesAccounts)
	if !before && len(all) > 0 {
		return nil
	}
	if ManagesAccounts(next) {
		return nil
	}
	for _, a := range all {
		if a.Username != next.Username && ManagesAccounts(a) {
			return nil
		}
	}
	return ErrLastAccountManager
}
