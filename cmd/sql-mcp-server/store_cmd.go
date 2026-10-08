package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/configstore"
	"github.com/nethinwei/sql-mcp-server/x/revisionops"
)

const storeUsage = "usage: sql-mcp-server store <init|import|list|show|diff|publish|rollback> [flags]"

// storeFlags are shared by every store subcommand.
type storeFlags struct {
	fs          *flag.FlagSet
	store       *string
	secretRoots *string
	author      *string
	timeout     *time.Duration
}

func newStoreFlags(name string) storeFlags {
	fs := flag.NewFlagSet("store "+name, flag.ContinueOnError)
	return storeFlags{
		fs:          fs,
		store:       fs.String("store", "", "configuration store <driver>:<dsn> (env SQL_MCP_STORE)"),
		secretRoots: fs.String("secret-root", "", "comma-separated allowed roots for ${file:...} in the store DSN"),
		author:      fs.String("author", "", "author recorded on the revision (default: OS user)"),
		timeout:     fs.Duration("timeout", 30*time.Second, "operation timeout"),
	}
}

func (f storeFlags) spec() (configstore.Spec, error) {
	raw := *f.store
	if raw == "" {
		raw = os.Getenv(storeEnv)
	}
	if raw == "" {
		return configstore.Spec{}, errors.New("--store (or SQL_MCP_STORE) is required")
	}
	return configstore.ParseSpec(raw)
}

func (f storeFlags) open(ctx context.Context) (*configstore.SQLStore, error) {
	spec, err := f.spec()
	if err != nil {
		return nil, err
	}
	return configstore.Open(ctx, spec, storeResolver(*f.secretRoots))
}

func (f storeFlags) authorName() string {
	if *f.author != "" {
		return *f.author
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return "unknown"
}

func runStore(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New(storeUsage)
	}
	commands := map[string]func(context.Context, []string, io.Writer) error{
		"init": runStoreInit, "import": runStoreImport, "list": runStoreList, "show": runStoreShow,
		"diff": runStoreDiff, "publish": runStorePublish, "rollback": runStoreRollback,
	}
	run, ok := commands[args[0]]
	if !ok {
		return fmt.Errorf("unknown store command %q; %s", args[0], storeUsage)
	}
	return run(ctx, args[1:], stdout)
}

// revisionOps returns the revision operations shared with the admin API.
func revisionOps(store revision.Store) revisionops.Service {
	return revisionops.Service{Store: store, Via: "cli"}
}

func runStoreInit(ctx context.Context, args []string, stdout io.Writer) error {
	f := newStoreFlags("init")
	if err := f.fs.Parse(args); err != nil {
		return err
	}
	spec, err := f.spec()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, *f.timeout)
	defer cancel()
	if err := configstore.Init(ctx, spec, storeResolver(*f.secretRoots)); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "initialized %s (schema version %d)\n", spec, configstore.SchemaVersion)
	return err
}

// storePayload loads, validates and normalizes a configuration file into a
// store payload under the store-mode secret rule.
func storePayload(path string) ([]byte, error) {
	cfg, err := bootstrap.Load(path)
	if err != nil {
		return nil, err
	}
	return revisionops.Normalize(cfg)
}

func runStoreImport(ctx context.Context, args []string, stdout io.Writer) error {
	f := newStoreFlags("import")
	path := f.fs.String("config", "", "configuration file to import")
	comment := f.fs.String("comment", "", "revision comment")
	if err := f.fs.Parse(args); err != nil {
		return err
	}
	if *path == "" {
		return errors.New("--config is required")
	}
	payload, err := storePayload(*path)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, *f.timeout)
	defer cancel()
	store, err := f.open(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	rev, unchanged, err := revisionOps(store).Draft(ctx, revisionops.DraftRequest{
		Payload: payload, Author: f.authorName(), Comment: *comment, SkipIfPublished: true,
	})
	if err != nil {
		return err
	}
	if unchanged {
		_, err = fmt.Fprintf(stdout, "unchanged: matches published revision %d\n", rev.ID)
		return err
	}
	_, err = fmt.Fprintf(stdout, "draft %d %s\n", rev.ID, rev.ContentHash)
	return err
}

func runStoreList(ctx context.Context, args []string, stdout io.Writer) error {
	f := newStoreFlags("list")
	limit := f.fs.Int("limit", 20, "maximum revisions to list (0 = all)")
	if err := f.fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, *f.timeout)
	defer cancel()
	store, err := f.open(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	revs, err := store.List(ctx, *limit)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "ID\tSTATE\tCREATED\tAUTHOR\tHASH\tCOMMENT")
	for _, r := range revs {
		_, _ = fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.State, r.CreatedAt.Format(time.RFC3339),
			r.Author, shortHash(r.ContentHash), r.Comment)
	}
	return w.Flush()
}

func shortHash(h string) string {
	h = strings.TrimPrefix(h, revision.HashPrefix)
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// resolveRevision returns revision id, or the published revision for
// "published".
func resolveRevision(ctx context.Context, store revision.Store, ref string) (revision.Revision, error) {
	if ref == "published" {
		return store.Published(ctx)
	}
	id, err := strconv.ParseInt(ref, 10, 64)
	if err != nil {
		return revision.Revision{}, fmt.Errorf("invalid revision %q: want an id or \"published\"", ref)
	}
	return store.Get(ctx, id)
}

func runStoreShow(ctx context.Context, args []string, stdout io.Writer) error {
	f := newStoreFlags("show")
	if err := f.fs.Parse(args); err != nil {
		return err
	}
	if f.fs.NArg() != 1 {
		return errors.New("usage: sql-mcp-server store show [flags] <id|published>")
	}
	ctx, cancel := context.WithTimeout(ctx, *f.timeout)
	defer cancel()
	store, err := f.open(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	rev, err := resolveRevision(ctx, store, f.fs.Arg(0))
	if err != nil {
		return err
	}
	if err := rev.Verify(); err != nil {
		return err
	}
	_, err = stdout.Write(rev.Payload)
	return err
}

func runStoreDiff(ctx context.Context, args []string, stdout io.Writer) error {
	f := newStoreFlags("diff")
	raw := f.fs.Bool("raw", false, "compare stored bytes instead of the current encoding")
	if err := f.fs.Parse(args); err != nil {
		return err
	}
	if f.fs.NArg() < 1 || f.fs.NArg() > 2 {
		return errors.New("usage: sql-mcp-server store diff [flags] <a> [<b>] (b defaults to published)")
	}
	ctx, cancel := context.WithTimeout(ctx, *f.timeout)
	defer cancel()
	store, err := f.open(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	a, err := resolveRevision(ctx, store, f.fs.Arg(0))
	if err != nil {
		return err
	}
	bRef := "published"
	if f.fs.NArg() == 2 {
		bRef = f.fs.Arg(1)
	}
	b, err := resolveRevision(ctx, store, bRef)
	if err != nil {
		return err
	}
	diff := revisionops.Diff
	if *raw {
		diff = revisionops.RawDiff
	}
	text, err := diff(fmt.Sprintf("revision %d", a.ID), a.Payload, fmt.Sprintf("revision %d", b.ID), b.Payload)
	if err != nil {
		return err
	}
	_, err = io.WriteString(stdout, text)
	return err
}

func runStorePublish(ctx context.Context, args []string, stdout io.Writer) error {
	f := newStoreFlags("publish")
	restart := f.fs.Bool("restart-required", false, "allow changes that only take effect after a restart")
	if err := f.fs.Parse(args); err != nil {
		return err
	}
	if f.fs.NArg() != 1 {
		return errors.New("usage: sql-mcp-server store publish [flags] <id>")
	}
	ctx, cancel := context.WithTimeout(ctx, *f.timeout)
	defer cancel()
	store, err := f.open(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	target, err := resolveRevision(ctx, store, f.fs.Arg(0))
	if err != nil {
		return err
	}
	rev, err := revisionOps(store).Publish(ctx, revisionops.PublishRequest{
		ID: target.ID, RestartRequired: *restart, Author: f.authorName(),
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "published %d %s\n", rev.ID, rev.ContentHash)
	return err
}

func runStoreRollback(ctx context.Context, args []string, stdout io.Writer) error {
	f := newStoreFlags("rollback")
	to := f.fs.Int64("to", 0, "revision to restore (default: newest earlier published content)")
	comment := f.fs.String("comment", "", "revision comment")
	restart := f.fs.Bool("restart-required", false, "allow changes that only take effect after a restart")
	if err := f.fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, *f.timeout)
	defer cancel()
	store, err := f.open(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	res, err := revisionOps(store).Rollback(ctx, revisionops.RollbackRequest{
		To: *to, RestartRequired: *restart, Author: f.authorName(), Comment: *comment,
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "rolled back %d; published %d (content of %d) %s\n",
		res.From, res.Published.ID, res.Target, res.Published.ContentHash)
	return err
}
