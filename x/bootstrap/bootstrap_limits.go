package bootstrap

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/core/entity"
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
func checkNativeLimits(cfg *config.Config, connections Connections, shared *Shared) error {
	issues := NativeLimitIssues(cfg, connections)
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
// not enforce (see limitIssues).
func NativeLimitIssues(cfg *config.Config, connections Connections) []LimitIssue {
	return limitIssues(cfg, func(datasource, connection string) (dialect.Profile, bool) {
		p, ok := connections[datasource][connection]
		if !ok {
			return nil, false
		}
		return p.Profile(), true
	})
}

// limitIssues lists, in a stable order, the statement timeouts the work cfg
// routes to each connection relies on but the connection, as profileOf
// judges it, does not enforce: reads on the read connection; writes and
// procedures, when some role or user may run them, on their routed
// connections, with reads on the write connection too when reads follow
// writes there (readAfterWrite, read-write transactions); and both on the SQL
// escape hatch's connection while the hatch is on. A connection profileOf
// does not know is skipped.
func limitIssues(
	cfg *config.Config, profileOf func(datasource, connection string) (dialect.Profile, bool),
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
			need(name, database.SQLEscape.Connection, dialect.TimeoutWrite, reason.UseSQLEscape)
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
	return NativeLimitIssues(a.config, a.Connections)
}

// PlannedLimitIssues lists the hard limits cfg, a configuration not yet
// applied, would rely on that its connections would not enforce, judged from
// each connection's DSN without connecting (its secrets resolved as serving
// resolves them). A connection that cannot be judged is an issue of its own.
// An App assembled from connections it was handed judges by the serving
// connection of the same name instead.
func (a *App) PlannedLimitIssues(cfg *config.Config) []LimitIssue {
	if a.scanResolver == nil {
		return NativeLimitIssues(cfg, a.Connections)
	}
	return limitIssues(cfg, func(datasource, connection string) (dialect.Profile, bool) {
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
