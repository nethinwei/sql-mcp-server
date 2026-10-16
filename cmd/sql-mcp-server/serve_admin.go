package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/bus"
	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/x/admin"
	"github.com/nethinwei/sql-mcp-server/x/admin/configapp"
	"github.com/nethinwei/sql-mcp-server/x/admin/graph"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
)

type serveAdminFlags struct {
	enabled, playground *bool
}

func newServeAdminFlags(fs *flag.FlagSet) serveAdminFlags {
	return serveAdminFlags{
		enabled:    fs.Bool("admin", false, "serve the admin API at /admin (store mode, HTTP only)"),
		playground: fs.Bool("admin-playground", false, "also serve GraphiQL at /admin/playground (development)"),
	}
}

// handler returns the admin API handler, or nil when --admin is not set. The
// admin API needs store mode (it writes revisions) and the HTTP transport.
func (f serveAdminFlags) handler(
	src serveSource,
	runtime *bootstrap.Runtime,
	execution *bus.Bus,
	cfg *config.Config,
	transport string,
	watching bool,
) (http.Handler, error) {
	if !*f.enabled {
		if *f.playground {
			return nil, errors.New("--admin-playground requires --admin")
		}
		return nil, nil
	}
	if src.store == nil {
		return nil, errors.New("--admin requires --store")
	}
	if transport != "http" {
		return nil, errors.New("--admin requires the http transport")
	}
	return admin.New(admin.Config{
		Store: src.store, Accounts: src.store, Catalog: src.store, Plans: src.store,
		Introspect: runtimeIntrospection(runtime),
		Bus:        execution,
		Status:     runtimeStatus(runtime, execution, src.rev, watching),
		Capabilities: func() bootstrap.EntityCapabilities {
			if app := runtime.Current(); app != nil {
				return app.Capabilities()
			}
			return nil
		},
		MissingTables: runtimeMissingTables(runtime),
		Profiles: func() ([]bootstrap.ConnectionProfile, []bootstrap.LimitIssue) {
			if app := runtime.Current(); app != nil {
				return app.Profiles(), app.LimitIssues()
			}
			return nil, nil
		},
		PlannedLimits: func(next *config.Config) []bootstrap.LimitIssue {
			if app := runtime.Current(); app != nil {
				return app.PlannedLimitIssues(next)
			}
			return nil
		},
		PathPrefix:   cfg.Server.PathPrefix,
		SecureCookie: secureCookie(cfg), Playground: *f.playground,
	}), nil
}

// secureCookie reports whether browsers reach the console over HTTPS: the
// server serves TLS, or a proxy terminating it is how clients reach it
// (server.publicURL is https).
func secureCookie(cfg *config.Config) bool {
	return cfg.Server.Auth.TLS.Cert != "" || strings.HasPrefix(strings.ToLower(cfg.Server.PublicURL), "https://")
}

// runtimeMissingTables reports the entities whose tables the served App found
// missing.
func runtimeMissingTables(runtime *bootstrap.Runtime) func() map[string]bool {
	return func() map[string]bool {
		if app := runtime.Current(); app != nil {
			return app.MissingTables()
		}
		return nil
	}
}

// runtimeStatus reports the served revision, the startup revision until the
// store watcher tracks it, and what runs on the execution bus.
func runtimeStatus(
	runtime *bootstrap.Runtime, execution *bus.Bus, startup int64, watching bool,
) func() graph.RuntimeState {
	return func() graph.RuntimeState {
		stats := execution.Stats()
		st := graph.RuntimeState{Applied: runtime.AppliedRevision(), Watching: watching, Execution: &stats}
		if st.Applied == 0 {
			st.Applied = startup
		}
		if stale, ok := runtime.Stale(); ok {
			st.Unapplied = &stale
		}
		if app := runtime.Current(); app != nil {
			st.Unavailable, st.Unreachable = app.Unavailable, app.Unreachable
		}
		return st
	}
}

// runtimeIntrospection runs fn with the introspector of datasource as
// database configures it, on a connection of its own; an App assembled from
// handed connections introspects its connected datasource instead. It runs
// on the bus's Admin lane, or under the admission ctx carries (a scan's).
func runtimeIntrospection(runtime *bootstrap.Runtime) configapp.Introspection {
	return func(
		ctx context.Context, datasource string, database config.DatabaseConfig, fn func(introspect.Introspector) error,
	) error {
		app := runtime.Current()
		if app == nil {
			return bootstrap.ErrRuntimeClosed
		}
		return app.Bus().Lane(bus.Admin).Ensure(ctx, func(ctx context.Context) error {
			return introspectOn(ctx, runtime, datasource, database, fn)
		})
	}
}

// introspectOn runs fn with the introspector of datasource; see
// runtimeIntrospection.
func introspectOn(
	ctx context.Context, runtime *bootstrap.Runtime, datasource string, database config.DatabaseConfig,
	fn func(introspect.Introspector) error,
) error {
	app, release, err := runtime.Acquire()
	if err != nil {
		return err
	}
	// The scan does not hold the snapshot, so serving and reloads do not
	// wait for it.
	provider, own, err := app.OpenScan(ctx, database)
	if own {
		release()
		if err != nil {
			return err
		}
		defer func() { _ = provider.Close() }()
	} else {
		defer release()
		var ok bool
		if provider, ok = app.Providers[datasource]; !ok {
			return fmt.Errorf("datasource %q is not connected", datasource)
		}
	}
	introspector := provider.Introspector()
	if introspector == nil {
		return fmt.Errorf("datasource %q does not support introspection", datasource)
	}
	return fn(introspector)
}
