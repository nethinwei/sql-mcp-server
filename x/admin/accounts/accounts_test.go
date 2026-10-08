package accounts

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/configstore"
)

const password = "a long enough password"

func TestCreateValidatesAndNormalizes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc := Service{Store: NewMemoryStore()}
	if _, err := svc.Create(ctx, "reader", password, []string{auth.PermRead}); !errors.Is(err, ErrLastAccountManager) {
		t.Fatalf("first account without admin:accounts: %v", err)
	}
	a, err := svc.Create(ctx, "  Root ", password, []string{auth.PermAll})
	if err != nil || a.Username != "root" || a.CreatedAt.IsZero() || !auth.VerifyPassword(a.PasswordHash, password) {
		t.Fatalf("create = %+v, %v", a, err)
	}
	if _, err := svc.Create(ctx, "root", password, nil); !errors.Is(err, configstore.ErrAdminExists) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := svc.Create(ctx, "x", "short", nil); err == nil {
		t.Fatal("weak password accepted")
	}
	if _, err := svc.Create(ctx, "x", password, []string{"admin:everything"}); err == nil {
		t.Fatal("unknown permission accepted")
	}
}

func TestLastAccountManagerIsKept(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc := Service{Store: NewMemoryStore()}
	if _, err := svc.Create(ctx, "root", password, []string{auth.PermAll}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update(ctx, "root", Patch{Disabled: new(true)}); !errors.Is(err, ErrLastAccountManager) {
		t.Fatalf("disable last manager: %v", err)
	}
	if _, err := svc.Update(ctx, "root", Patch{Permissions: []string{auth.PermRead}}); !errors.Is(
		err,
		ErrLastAccountManager,
	) {
		t.Fatalf("demote last manager: %v", err)
	}
	if _, err := svc.Create(ctx, "ops", password, []string{auth.PermAccounts}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update(ctx, "root", Patch{Disabled: new(true)}); err != nil {
		t.Fatalf("disable with another manager: %v", err)
	}
	if _, err := svc.SetPassword(ctx, "ghost", password); !errors.Is(err, configstore.ErrAdminNotFound) {
		t.Fatalf("unknown account: %v", err)
	}
}

// A store that already lacks a manager (for example after editing the table
// by hand) accepts the changes that recover it.
func TestRecoveryIsNeverBlocked(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc := Service{
		Store: NewMemoryStore(Account{Username: "root", Permissions: []string{auth.PermAll}, Disabled: true}),
	}
	if _, err := svc.Update(ctx, "root", Patch{Disabled: new(false)}); err != nil {
		t.Fatalf("enable: %v", err)
	}
}

// Two managers disabling each other at once: exactly one wins, on the SQL
// store whose MutateAdmins serializes the check and the write.
func TestConcurrentDisableKeepsAManager(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	spec, err := configstore.ParseSpec("sqlite:" + filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := configstore.Init(ctx, spec, nil); err != nil {
		t.Fatal(err)
	}
	store, err := configstore.Open(ctx, spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc := Service{Store: store}
	for _, name := range []string{"alice", "bob"} {
		if _, err := svc.Create(ctx, name, password, []string{auth.PermAccounts}); err != nil {
			t.Fatal(err)
		}
	}
	errs := make(chan error, 2)
	for _, name := range []string{"alice", "bob"} {
		go func() {
			_, err := svc.Update(ctx, name, Patch{Disabled: new(true)})
			errs <- err
		}()
	}
	failed := 0
	for range 2 {
		if err := <-errs; errors.Is(err, ErrLastAccountManager) {
			failed++
		} else if err != nil {
			t.Fatal(err)
		}
	}
	all, err := svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	managers := 0
	for _, a := range all {
		if ManagesAccounts(a) {
			managers++
		}
	}
	if failed != 1 || managers != 1 {
		t.Fatalf("failed = %d, managers left = %d; want exactly one of each", failed, managers)
	}
}
