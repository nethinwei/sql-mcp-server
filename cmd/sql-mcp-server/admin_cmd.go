package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/configstore"
)

const adminUsage = "usage: sql-mcp-server admin <create|passwd|set|list> [flags]"

func runAdmin(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New(adminUsage)
	}
	commands := map[string]func(context.Context, []string, io.Writer) error{
		"create": runAdminCreate, "passwd": runAdminPasswd, "set": runAdminSet, "list": runAdminList,
	}
	run, ok := commands[args[0]]
	if !ok {
		return fmt.Errorf("unknown admin command %q; %s", args[0], adminUsage)
	}
	return run(ctx, args[1:], stdout)
}

// readPassword reads one line from stdin; passwords never come from flags,
// which would leak them into shell history and process listings.
func readPassword() (string, error) {
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	password := strings.TrimRight(line, "\r\n")
	if err := auth.ValidatePassword(password); err != nil {
		return "", err
	}
	return password, nil
}

func splitPermissions(raw string) []string {
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func openAdminStore(ctx context.Context, f storeFlags) (*configstore.SQLStore, context.CancelFunc, error) {
	ctx, cancel := context.WithTimeout(ctx, *f.timeout)
	store, err := f.open(ctx)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return store, cancel, nil
}

func runAdminCreate(ctx context.Context, args []string, stdout io.Writer) error {
	f := newStoreFlags("admin create")
	username := f.fs.String("username", "", "account name")
	perms := f.fs.String("permissions", auth.PermAll, "comma-separated admin permissions")
	if err := f.fs.Parse(args); err != nil {
		return err
	}
	if err := auth.ValidateUsername(*username); err != nil {
		return err
	}
	permissions := splitPermissions(*perms)
	if err := auth.ValidatePermissions(permissions); err != nil {
		return err
	}
	password, err := readPassword()
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	store, cancel, err := openAdminStore(ctx, f)
	if err != nil {
		return err
	}
	defer cancel()
	defer func() { _ = store.Close() }()
	a, err := store.CreateAdmin(ctx, configstore.AdminAccount{
		Username: *username, PasswordHash: hash, Permissions: permissions,
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "created admin %s (%s)\n", a.Username, strings.Join(a.Permissions, ","))
	return err
}

func runAdminPasswd(ctx context.Context, args []string, stdout io.Writer) error {
	f := newStoreFlags("admin passwd")
	username := f.fs.String("username", "", "account name")
	if err := f.fs.Parse(args); err != nil {
		return err
	}
	password, err := readPassword()
	if err != nil {
		return err
	}
	store, cancel, err := openAdminStore(ctx, f)
	if err != nil {
		return err
	}
	defer cancel()
	defer func() { _ = store.Close() }()
	a, err := store.GetAdmin(ctx, *username)
	if err != nil {
		return err
	}
	if a.PasswordHash, err = auth.HashPassword(password); err != nil {
		return err
	}
	if _, err := store.UpdateAdmin(ctx, a); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "password updated for %s\n", a.Username)
	return err
}

func runAdminSet(ctx context.Context, args []string, stdout io.Writer) error {
	f := newStoreFlags("admin set")
	username := f.fs.String("username", "", "account name")
	perms := f.fs.String("permissions", "", "replace permissions (comma-separated)")
	disable := f.fs.Bool("disable", false, "disable the account")
	enable := f.fs.Bool("enable", false, "enable the account")
	if err := f.fs.Parse(args); err != nil {
		return err
	}
	if *disable && *enable {
		return errors.New("--disable and --enable are mutually exclusive")
	}
	store, cancel, err := openAdminStore(ctx, f)
	if err != nil {
		return err
	}
	defer cancel()
	defer func() { _ = store.Close() }()
	a, err := store.GetAdmin(ctx, *username)
	if err != nil {
		return err
	}
	if *perms != "" {
		a.Permissions = splitPermissions(*perms)
		if err := auth.ValidatePermissions(a.Permissions); err != nil {
			return err
		}
	}
	if *disable || *enable {
		a.Disabled = *disable
	}
	if a, err = store.UpdateAdmin(ctx, a); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "updated admin %s (%s, disabled=%v)\n", a.Username,
		strings.Join(a.Permissions, ","), a.Disabled)
	return err
}

func runAdminList(ctx context.Context, args []string, stdout io.Writer) error {
	f := newStoreFlags("admin list")
	if err := f.fs.Parse(args); err != nil {
		return err
	}
	store, cancel, err := openAdminStore(ctx, f)
	if err != nil {
		return err
	}
	defer cancel()
	defer func() { _ = store.Close() }()
	all, err := store.ListAdmins(ctx)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "USERNAME\tPERMISSIONS\tDISABLED\tUPDATED")
	for _, a := range all {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%v\t%s\n", a.Username, strings.Join(a.Permissions, ","), a.Disabled,
			a.UpdatedAt.Format(time.RFC3339))
	}
	return w.Flush()
}
