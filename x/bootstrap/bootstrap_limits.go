package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/core/reason"
	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
)

// LimitIssue is a hard limit a datasource relies on that the connection its
// work routes to does not enforce on the database side: a statement may run
// on past cost.queryTimeout there.
type LimitIssue struct {
	Datasource string
	Connection string
	Key        dialect.Key
	Claim      dialect.Claim
	// Use is the work relying on it: reads, writes, procedures or the SQL
	// escape hatch.
	Use reason.Reason
}

// Notice is the issue as validating a configuration reports it.
func (i LimitIssue) Notice() reason.Reason {
	return reason.Compose(reason.LimitNotEnforced,
		map[string]reason.Reason{"use": i.Use, "strength": i.Claim.Strength.Reason(), "cause": i.Claim.Reason},
		"datasource", i.Datasource, "connection", i.Connection, "key", string(i.Key))
}

func (i LimitIssue) String() string {
	return fmt.Sprintf("datasource %q, connection %q: %s for %s is %s (%s)",
		i.Datasource, i.Connection, i.Key, i.Use, i.Claim.Strength, i.Claim.Reason)
}

// ErrNativeLimits marks a configuration refused under cost.requireNativeLimits.
var ErrNativeLimits = errors.New(
	"cost.requireNativeLimits: a datasource relies on a limit its connection does not enforce")

// checkNativeLimits refuses the configuration when cost.requireNativeLimits
// is on and there is a LimitIssue; otherwise it warns about each, once for as
// long as they stay the same in the process of shared.
func checkNativeLimits(cfg *config.Config, connections Connections, readOnly ReadOnly, shared *Shared) error {
	issues := NativeLimitIssues(cfg, connections, readOnly)
	lines := make([]string, len(issues))
	for i, issue := range issues {
		lines[i] = issue.String()
	}
	if cfg.Cost.RequireNativeLimits && len(issues) > 0 {
		return fmt.Errorf("%w:\n  %s", ErrNativeLimits, strings.Join(lines, "\n  "))
	}
	if !shared.unwarned(strings.Join(lines, "\n")) {
		return nil
	}
	for _, line := range lines {
		slog.Warn("a hard limit is not enforced on the database side", "detail", line)
	}
	return nil
}

// NativeLimitIssues lists, in a stable order, the statement timeouts the
// work cfg routes to each of connections relies on but the connection does
// not enforce (see limitIssues); readOnly are the connections known not to
// write.
func NativeLimitIssues(cfg *config.Config, connections Connections, readOnly ReadOnly) []LimitIssue {
	return limitIssues(cfg, readOnly, func(datasource, connection string) (dialect.Profile, bool) {
		p, ok := connections[datasource][connection]
		if !ok {
			return nil, false
		}
		return p.Profile(), true
	})
}

// ReadOnly are the connections, by datasource and connection name, whose
// account is known not to write: the server refuses writes (read_only, a
// replica) or the account holds no privilege that writes.
type ReadOnly map[string]map[string]bool

// writeProbeTimeout bounds asking a connection whether it can write.
const writeProbeTimeout = 5 * time.Second

// readOnlyEscapes asks each SQL escape hatch connection, while the hatch is
// on, whether its account can write: one that cannot needs no statement
// timeout for writes. A connection that does not say is not read-only.
func readOnlyEscapes(cfg *config.Config, connections Connections) ReadOnly {
	out := ReadOnly{}
	if !cfg.Tools.ExecuteSQL {
		return out
	}
	for name, database := range cfg.Databases {
		connection := database.SQLEscape.Connection
		p := connections[name][connection]
		if connection == "" || p == nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), writeProbeTimeout)
		if cannotWrite(ctx, p.Introspector()) {
			if out[name] == nil {
				out[name] = map[string]bool{}
			}
			out[name][connection] = true
		}
		cancel()
	}
	return out
}

// cannotWrite reports a connection whose server refuses writes or whose
// account holds no privilege that writes, as its introspector reports.
func cannotWrite(ctx context.Context, i introspect.Introspector) bool {
	inspect, ok := i.(introspect.PrivilegeInspector)
	if !ok {
		return false
	}
	if readOnly, err := inspect.ReadOnly(ctx); err == nil && readOnly {
		return true
	}
	writes, ok := i.(introspect.WriteInspector)
	if !ok {
		return false
	}
	can, err := writes.CanWrite(ctx)
	return err == nil && can == introspect.PrivilegeDenied
}

// limitIssues lists, in a stable order, the statement timeouts the work cfg
// routes to each connection relies on but the connection, as profileOf
// judges it, does not enforce: reads on the read connection; writes and
// procedures, when some role or user may run them, on their routed
// connections, with reads on the write connection too when reads follow
// writes there (readAfterWrite, read-write transactions); and both on the SQL
// escape hatch's connection while the hatch is on (writes only if its account
// can write: see readOnlyEscapes). A connection profileOf does not know is
// skipped.
func limitIssues(
	cfg *config.Config, readOnly ReadOnly, profileOf func(datasource, connection string) (dialect.Profile, bool),
) []LimitIssue {
	writes, procedures := writingDatasources(cfg)
	var out []LimitIssue
	need := func(datasource, connection string, key dialect.Key, code reason.Code) {
		use := reason.New(code)
		p, ok := profileOf(datasource, connection)
		if !ok {
			return
		}
		if claim := p.Claim(key); claim.Strength != dialect.Enforced {
			out = append(out, LimitIssue{Datasource: datasource, Connection: connection, Key: key, Claim: claim, Use: use})
		}
	}
	for name, database := range cfg.Databases {
		route := database.Route()
		need(name, route.Read, dialect.TimeoutRead, reason.UseReads)
		if writes[name] {
			need(name, route.Write, dialect.TimeoutWrite, reason.UseWrites)
			if route.Write != route.Read && (database.ReadAfterWrite > 0 || cfg.Tools.BeginTransaction) {
				need(name, route.Write, dialect.TimeoutRead, reason.UseReadsAfterWrite)
			}
		}
		if procedures[name] {
			need(name, route.Execute, dialect.TimeoutWrite, reason.UseProcedures)
		}
		if cfg.Tools.ExecuteSQL && database.SQLEscape.Connection != "" {
			need(name, database.SQLEscape.Connection, dialect.TimeoutRead, reason.UseSQLEscape)
			if !readOnly[name][database.SQLEscape.Connection] {
				need(name, database.SQLEscape.Connection, dialect.TimeoutWrite, reason.UseSQLEscape)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// writingDatasources are the datasources with an entity some role or user
// may write through a write tool that is on, and those with a procedure some
// role or user may execute through a tool that exposes it.
func writingDatasources(cfg *config.Config) (writes, procedures map[string]bool) {
	writes, procedures = map[string]bool{}, map[string]bool{}
	entities := make(map[string]config.EntityConfig, len(cfg.Entities))
	for _, e := range cfg.Entities {
		entities[e.ID()] = e
	}
	tools := map[entity.Action]bool{
		entity.ActionCreate: cfg.Tools.CreateRecord, entity.ActionUpdate: cfg.Tools.UpdateRecord,
		entity.ActionDelete: cfg.Tools.DeleteRecord,
	}
	for id, actions := range grantedActions(cfg) {
		e, ok := entities[id]
		if !ok {
			continue
		}
		for _, a := range actions {
			switch {
			case tools[a] && e.MCP.DMLTools:
				writes[e.DatasourceName()] = true
			case a == entity.ActionExecute && executable(cfg, e):
				procedures[e.DatasourceName()] = true
			}
		}
	}
	return writes, procedures
}

// executable reports whether a tool exposes procedure e: execute_entity, or
// a tool of its own.
func executable(cfg *config.Config, e config.EntityConfig) bool {
	return e.MCP.TrustedProcedure && (e.MCP.CustomTool || cfg.Tools.ExecuteEntity && e.MCP.DMLTools)
}

// ConnectionProfile is what one serving connection guarantees.
type ConnectionProfile struct {
	Datasource string
	Connection string
	Profile    dialect.Profile
}

// Profiles lists what each serving connection guarantees, by datasource and
// connection name.
func (a *App) Profiles() []ConnectionProfile {
	var out []ConnectionProfile
	for datasource, byName := range a.Connections {
		for connection, p := range byName {
			out = append(out, ConnectionProfile{Datasource: datasource, Connection: connection, Profile: p.Profile()})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Datasource != out[j].Datasource {
			return out[i].Datasource < out[j].Datasource
		}
		return out[i].Connection < out[j].Connection
	})
	return out
}

// LimitIssues lists the hard limits the serving configuration relies on that
// its connections do not enforce (see NativeLimitIssues).
func (a *App) LimitIssues() []LimitIssue {
	if a.config == nil {
		return nil
	}
	return NativeLimitIssues(a.config, a.Connections, a.readOnly)
}

// PlannedLimitIssues lists the hard limits cfg, a configuration not yet
// applied, would rely on that its connections would not enforce, judged from
// each connection's DSN without connecting (its secrets resolved as serving
// resolves them). A connection that cannot be judged is an issue of its own.
// An App assembled from connections it was handed judges by the serving
// connection of the same name instead.
func (a *App) PlannedLimitIssues(cfg *config.Config) []LimitIssue {
	if a.scanResolver == nil {
		return NativeLimitIssues(cfg, a.Connections, a.readOnly)
	}
	return limitIssues(cfg, a.plannedReadOnly(cfg), func(datasource, connection string) (dialect.Profile, bool) {
		database := cfg.Databases[datasource]
		c, ok := database.ConnectionsOrDSN()[connection]
		if !ok {
			return nil, false
		}
		profile, err := plannedLimits(a.scanResolver, database.Driver, c, cfg.Cost.QueryTimeout)
		if err != nil {
			unknown := dialect.Claim{Strength: dialect.Unsupported, Evidence: dialect.Declared,
				Reason: reason.New(reason.LimitUnjudged, "error", err.Error())}
			return dialect.Profile{dialect.TimeoutRead: unknown, dialect.TimeoutWrite: unknown}, true
		}
		return profile, true
	})
}

// plannedLimits judges connection c of a driver database from its DSN.
func plannedLimits(
	r SecretResolver, driver string, c config.ConnectionConfig, timeout time.Duration,
) (dialect.Profile, error) {
	dsn, err := r.Resolve(c.DSN)
	if err != nil {
		return nil, err
	}
	return providerregistry.Limits(driver, dsn, providerregistry.Options{Timeout: timeout, Pooler: c.Pooler})
}

// plannedReadOnly are the serving read-only connections cfg keeps as they
// are (the same DSN): a connection it changes is not known to be read-only.
func (a *App) plannedReadOnly(cfg *config.Config) ReadOnly {
	out := ReadOnly{}
	if a.config == nil {
		return out
	}
	for datasource, byName := range a.readOnly {
		for connection := range byName {
			was := a.config.Databases[datasource].ConnectionsOrDSN()[connection]
			now, ok := cfg.Databases[datasource].ConnectionsOrDSN()[connection]
			if ok && now.DSN == was.DSN && now.Pooler == was.Pooler {
				if out[datasource] == nil {
					out[datasource] = map[string]bool{}
				}
				out[datasource][connection] = true
			}
		}
	}
	return out
}
