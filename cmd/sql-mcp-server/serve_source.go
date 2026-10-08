package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/configstore"
)

// storeEnv names the environment variable equivalent of --store.
const storeEnv = "SQL_MCP_STORE"

// defaultStoreWatchInterval applies to --watch in store mode unless
// --watch-interval is set explicitly.
const defaultStoreWatchInterval = 5 * time.Second

// serveSource is where serve reads its configuration: a file, or the
// published revision of a configuration store.
type serveSource struct {
	startup *config.Config
	path    string
	store   *configstore.SQLStore
	rev     int64
}

func (s serveSource) close() {
	if s.store != nil {
		_ = s.store.Close()
	}
}

// openServeSource loads the startup configuration. --store (or SQL_MCP_STORE)
// selects store mode, which cannot be combined with an explicit --config.
func openServeSource(
	ctx context.Context,
	path, storeFlag, secretRoots string,
	configExplicit bool,
) (serveSource, error) {
	raw := storeFlag
	if raw == "" {
		raw = os.Getenv(storeEnv)
	}
	if raw == "" {
		cfg, err := bootstrap.Load(path)
		return serveSource{startup: cfg, path: path}, err
	}
	if configExplicit {
		return serveSource{}, errors.New("--config and --store are mutually exclusive")
	}
	store, err := openStore(ctx, raw, secretRoots)
	if err != nil {
		return serveSource{}, err
	}
	rev, err := store.Published(ctx)
	if errors.Is(err, revision.ErrNoPublished) {
		_ = store.Close()
		return serveSource{}, errors.New(
			"config store has no published revision; run `store import` and `store publish` first")
	}
	if err != nil {
		_ = store.Close()
		return serveSource{}, err
	}
	cfg, err := bootstrap.LoadRevision(rev)
	if err != nil {
		_ = store.Close()
		return serveSource{}, fmt.Errorf("published revision %d: %w", rev.ID, err)
	}
	return serveSource{startup: cfg, store: store, rev: rev.ID}, nil
}

// openStore parses and opens a store spec; file placeholders in its DSN are
// limited to the comma-separated secret roots.
func openStore(ctx context.Context, raw, secretRoots string) (*configstore.SQLStore, error) {
	spec, err := configstore.ParseSpec(raw)
	if err != nil {
		return nil, err
	}
	return configstore.Open(ctx, spec, storeResolver(secretRoots))
}

func storeResolver(secretRoots string) configstore.Resolver {
	var roots []string
	for _, root := range strings.Split(secretRoots, ",") {
		if root = strings.TrimSpace(root); root != "" {
			roots = append(roots, root)
		}
	}
	return bootstrap.EnvFileResolver{AllowedRoots: roots}.Resolve
}

// watch reloads on file changes or newly published store revisions.
func (s serveSource) watch(
	ctx context.Context,
	runtime *bootstrap.Runtime,
	build func(*config.Config) (*bootstrap.App, error),
	interval time.Duration,
	intervalExplicit bool,
) {
	if s.store == nil {
		serveConfigWatcher(ctx, runtime, s.path, interval)
		return
	}
	if !intervalExplicit {
		interval = defaultStoreWatchInterval
	}
	err := runtime.WatchStore(ctx, s.store, s.rev, interval, func(rev revision.Revision) (*bootstrap.App, error) {
		next, err := bootstrap.LoadRevision(rev)
		if err != nil {
			return nil, err
		}
		return build(next)
	}, func(err error) {
		slog.Error("config store reload failed; keeping previous snapshot", "error", err.Error())
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("config store watcher stopped", "error", err.Error())
	}
}
