package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/configstore"
	"github.com/nethinwei/sql-mcp-server/x/revisionops"
)

// runMigrate copies configuration between a YAML file ("file:<path>") and
// stores ("<driver>:<dsn>"), checking the content hash afterwards.
func runMigrate(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	from := fs.String("from", "", "source: file:<path> or <driver>:<dsn>")
	to := fs.String("to", "", "destination: file:<path> or <driver>:<dsn>")
	history := fs.Bool("history", false, "copy every revision between stores (destination must be empty)")
	secretRoots := fs.String("secret-root", "", "comma-separated allowed roots for ${file:...} in store DSNs")
	author := fs.String("author", "", "author recorded on a created revision (default: OS user)")
	timeout := fs.Duration("timeout", 5*time.Minute, "operation timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *from == "" || *to == "" {
		return errors.New("usage: sql-mcp-server migrate --from <file:path|driver:dsn> --to <file:path|driver:dsn>")
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	src, err := openMigrateEndpoint(ctx, *from, *secretRoots)
	if err != nil {
		return fmt.Errorf("--from: %w", err)
	}
	defer src.close()
	dst, err := openMigrateEndpoint(ctx, *to, *secretRoots)
	if err != nil {
		return fmt.Errorf("--to: %w", err)
	}
	defer dst.close()
	if *history {
		if src.store == nil || dst.store == nil {
			return errors.New("--history requires a store on both sides")
		}
		return migrateHistory(ctx, src.store, dst.store, stdout)
	}
	payload, err := src.read(ctx)
	if err != nil {
		return err
	}
	who := storeFlags{author: author}.authorName()
	if err := dst.write(ctx, payload, who, "migrated from "+src.label); err != nil {
		return err
	}
	written, err := dst.read(ctx)
	if err != nil {
		return err
	}
	if revision.Hash(written) != revision.Hash(payload) {
		return fmt.Errorf("content hash mismatch after migration: %s != %s", revision.Hash(written), revision.Hash(payload))
	}
	_, err = fmt.Fprintf(stdout, "migrated %s -> %s %s\n", src.label, dst.label, revision.Hash(payload))
	return err
}

type migrateEndpoint struct {
	label string
	path  string
	store revision.Store
}

func openMigrateEndpoint(ctx context.Context, raw, secretRoots string) (migrateEndpoint, error) {
	if path, ok := strings.CutPrefix(raw, "file:"); ok {
		if path == "" {
			return migrateEndpoint{}, errors.New("file: needs a path")
		}
		return migrateEndpoint{label: "file:" + path, path: path}, nil
	}
	spec, err := configstore.ParseSpec(raw)
	if err != nil {
		return migrateEndpoint{}, err
	}
	store, err := configstore.Open(ctx, spec, storeResolver(secretRoots))
	if err != nil {
		return migrateEndpoint{}, err
	}
	return migrateEndpoint{label: spec.String(), store: store}, nil
}

func (e migrateEndpoint) close() {
	if e.store != nil {
		_ = e.store.Close()
	}
}

// read returns the normalized payload: a file is validated under the store
// secret rule; a store yields its published revision.
func (e migrateEndpoint) read(ctx context.Context) ([]byte, error) {
	if e.store == nil {
		return storePayload(e.path)
	}
	rev, err := e.store.Published(ctx)
	if err != nil {
		return nil, err
	}
	if err := rev.Verify(); err != nil {
		return nil, err
	}
	return rev.Payload, nil
}

// write stores payload: a file must not exist yet; a store gets a new
// revision published on top of its current one.
func (e migrateEndpoint) write(ctx context.Context, payload []byte, author, comment string) error {
	if e.store == nil {
		file, err := os.OpenFile(e.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if _, err := file.Write(payload); err != nil {
			_ = file.Close()
			return err
		}
		return file.Close()
	}
	ops := revisionOps(e.store)
	draft, unchanged, err := ops.Draft(ctx, revisionops.DraftRequest{
		Payload: payload, Author: author, Comment: comment, SkipIfPublished: true,
	})
	if err != nil || unchanged {
		return err
	}
	// The target store need not be serving yet, so restart-only changes are
	// allowed; they apply when a server starts from it.
	_, err = ops.Publish(ctx, revisionops.PublishRequest{ID: draft.ID, RestartRequired: true, Author: author})
	return err
}

func migrateHistory(ctx context.Context, src, dst revision.Store, stdout io.Writer) error {
	summaries, err := src.List(ctx, 0)
	if err != nil {
		return err
	}
	revs := make([]revision.Revision, 0, len(summaries))
	for i := len(summaries) - 1; i >= 0; i-- {
		rev, err := src.Get(ctx, summaries[i].ID)
		if err != nil {
			return err
		}
		revs = append(revs, rev)
	}
	if err := dst.ImportHistory(ctx, revs); err != nil {
		return err
	}
	for _, want := range revs {
		got, err := dst.Get(ctx, want.ID)
		if err != nil {
			return err
		}
		if got.ContentHash != want.ContentHash || got.State != want.State {
			return fmt.Errorf("revision %d differs after migration", want.ID)
		}
	}
	_, err = fmt.Fprintf(stdout, "migrated %d revisions\n", len(revs))
	return err
}
