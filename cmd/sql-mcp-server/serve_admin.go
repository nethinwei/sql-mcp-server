package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/x/admin"
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
		Store: src.store, Accounts: src.store, Introspect: runtimeIntrospection(runtime),
		Status: runtimeStatus(runtime, src.rev, watching),
		Capabilities: func() bootstrap.EntityCapabilities {
			if app := runtime.Current(); app != nil {
				return app.Capabilities()
			}
			return nil
		},
		SecureCookie: cfg.Server.Auth.TLS.Cert != "", Playground: *f.playground,
	}), nil
}

// runtimeStatus reports the served revision: the startup revision until the
// store watcher tracks it.
func runtimeStatus(runtime *bootstrap.Runtime, startup int64, watching bool) func() graph.RuntimeState {
	return func() graph.RuntimeState {
		st := graph.RuntimeState{Applied: runtime.AppliedRevision(), Watching: watching}
		if st.Applied == 0 {
			st.Applied = startup
		}
		if stale, ok := runtime.Stale(); ok {
			st.Pending = &stale
		}
		return st
	}
}

// runtimeIntrospection runs fn with the introspector of datasource as
// database configures it, on a connection of its own; an App assembled from
// handed connections introspects its connected datasource instead.
func runtimeIntrospection(runtime *bootstrap.Runtime) graph.Introspection {
	return func(
		ctx context.Context, datasource string, database config.DatabaseConfig, fn func(introspect.Introspector) error,
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
}
