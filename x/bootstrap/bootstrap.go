package bootstrap

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/audit"
	"github.com/nethinwei/sql-mcp-server/core/budget"
	"github.com/nethinwei/sql-mcp-server/core/bus"
	"github.com/nethinwei/sql-mcp-server/core/cache"
	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/engine"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/hook"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/core/mask"
	coreprovider "github.com/nethinwei/sql-mcp-server/core/provider"
	"github.com/nethinwei/sql-mcp-server/core/ratelimit"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/core/tool"
	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
)

// ErrUnsupportedDriver is returned for a driver with no provider yet.
var ErrUnsupportedDriver = providerregistry.ErrUnsupportedDriver

// Provider aggregates the core interfaces a database adapter must satisfy.
type Provider = coreprovider.Provider

// App is the assembled application, ready to serve.
type App struct {
	// Provider is the read connection of the default datasource (the one a
	// request names none of).
	Provider Provider
	// Providers holds each datasource's read connection, which also serves
	// introspection and EXPLAIN; Connections holds every connection.
	Providers   map[string]Provider
	Connections Connections
	Prepared    map[string]*store.PreparedDB
	Sources     map[string]tool.DataSource
	// Escapes are the datasources admitting the SQL escape hatch, routed to
	// the connection each names.
	Escapes map[string]tool.EscapeRoute
	// defaultSource names the default datasource in Sources.
	defaultSource string
	Registry      *entity.Registry
	Authorizer    rbac.Authorizer
	Masker        mask.Masker
	Engine        *engine.Engine
	Tools         *tool.Registry
	ToolFlags     config.ToolFlags
	DefaultRole   string
	DefaultUser   string
	// Unavailable maps the ID of each entity whose table or columns are
	// missing from the database (see entity.Entity.Unavailable) to why.
	Unavailable map[string]string
	// Unreachable maps each datasource whose schema could not be read to the
	// error; its entities are unavailable until a reload reads it.
	Unreachable map[string]string
	// callers are each caller's limits (see callerLimits); keyed bounds
	// their rates and calls in flight (the process's when shared).
	callers map[string]config.PrincipalLimits
	keyed   *ratelimit.Keyed
	// Drift is raised when a call finds the schema of a datasource changed
	// (the process's when shared): WatchStore assembles the revision again.
	Drift *tool.SchemaDrift
	// Users maps an enabled user name to its identity; UserTokens maps a
	// tokenHash, and UserCerts a certificate subject (see
	// config.CanonicalCertSubject), to the user name. They follow the
	// snapshot on reload.
	Users map[string]UserIdentity
	// held reports a user its runtime holds (see holdChanged): one no
	// lookup of this snapshot finds either, the default user included.
	held         func(name string) bool
	UserTokens   map[string]string
	UserCerts    map[string]string
	Limits       tool.Limits
	Auditor      audit.Auditor
	Hooks        *hook.Hooks
	Cache        cache.Cache[[]map[string]any]
	Feedback     cost.FeedbackStore
	Budget       *budget.MemoryManager
	Transactions *tool.TransactionManager
	Writes       *tool.WriteTracker
	ResultMemory *tool.ResultMemory
	TxBeginners  map[string]store.TxBeginner
	// WriteTargets are what a write through each entity invalidates.
	WriteTargets *tool.WriteTargets
	// shared are the services the App shares with the other configurations
	// of its process (see Shared), and generation is its configuration's
	// generation there; releaseConnections returns its shared connections.
	shared             *Shared
	generation         uint64
	releaseConnections func()
	closeMu            sync.Mutex
	closed             bool
	// capabilities are assessed in the background once the App is assembled
	// (see Capabilities); stopAssessing ends that work and assessed reports
	// it ended.
	capabilities atomic.Pointer[EntityCapabilities]
	// config is the configuration the App was assembled from; scanResolver
	// resolves its secrets to open scan connections (see OpenScan).
	config        *config.Config
	scanResolver  SecretResolver
	stopAssessing context.CancelFunc
	assessed      chan struct{}
	// readOnly are the SQL escape hatch connections that cannot write (see
	// readOnlyEscapes).
	readOnly ReadOnly
}

// publishShared applies the App's configuration to the services it shares
// with the other configurations of its process, as it is published.
func (a *App) publishShared() {
	if a.shared != nil {
		a.shared.apply(a)
	}
}

// AuditDropped counts the audit events dropped for a full queue: by every
// App of the process with shared services (it does not start again at a
// reload), else by this App's auditor.
func (a *App) AuditDropped() int64 {
	if a.shared != nil {
		return a.shared.auditDropped.Load()
	}
	if dropping, ok := a.Auditor.(interface{ Dropped() int64 }); ok {
		return dropping.Dropped()
	}
	return 0
}

// Bus returns the execution bus the App runs on; nil for an App assembled
// without shared services, whose work then runs unbounded by a bus.
func (a *App) Bus() *bus.Bus {
	if a.shared == nil {
		return nil
	}
	return a.shared.bus
}

// Config returns the configuration the App was assembled from.
func (a *App) Config() *config.Config { return a.config }

// ToolContext builds a per-request tool.Context for the given role (a
// principal key), under its limits (see callerLimits).
func (a *App) ToolContext(role string) tool.Context {
	src := a.Sources[a.defaultSource]
	s := a.Services()
	if p, ok := a.callers[role]; ok {
		s.Limits = toolLimitsFor(a.Limits, p)
	}
	return tool.Context{
		Services: s, Role: role,
		DB: a.Provider, Dialect: src.Dialect, Gate: src.Gate, Analyze: src.Analyze,
	}
}

// Services are the App's, as every call runs on them.
func (a *App) Services() *tool.Services {
	s := &tool.Services{
		Registry: a.Registry, Authorizer: a.Authorizer, Masker: a.Masker, Cache: a.Cache, Engine: a.Engine,
		Auditor: a.Auditor, Hooks: a.Hooks, Limits: a.Limits, Feedback: a.Feedback, Sources: a.Sources,
		Transactions: a.Transactions, Writes: a.Writes, ResultMemory: a.ResultMemory, TxBeginners: a.TxBeginners,
		Generation: a.generation, WriteTargets: a.WriteTargets, Escapes: a.Escapes, Callers: a.keyed,
		Drift: a.Drift,
	}
	if a.Budget != nil { // a nil *MemoryManager must stay a nil interface
		s.Budget = a.Budget
	}
	return s
}

// ToolContextForSubject builds a per-request tool.Context for a role plus
// subject attributes (referenced by row-level ${subject.x} policies).
func (a *App) ToolContextForSubject(role string, subject map[string]any) tool.Context {
	tc := a.ToolContext(role)
	tc.Subject = subject
	if name, ok := strings.CutPrefix(role, config.UserPrincipalPrefix); ok {
		tc.User = name
		tc.UserRoles = a.Users[name].Roles
	}
	return tc
}

// stopAssessment cancels the assessment of capabilities and waits for it to
// end with its queries, or for ctx: one stuck must not keep the rest open.
func (a *App) stopAssessment(ctx context.Context) error {
	if a.stopAssessing == nil {
		return nil
	}
	a.stopAssessing()
	select {
	case <-a.assessed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// CloseContext stops engine admission and drains all execution before rolling
// back transactions and releasing audit/provider resources.
func (a *App) CloseContext(ctx context.Context) error {
	a.closeMu.Lock()
	defer a.closeMu.Unlock()
	if a.closed {
		return nil
	}
	var errs []error
	if err := a.stopAssessment(ctx); err != nil {
		errs = append(errs, err)
	}
	if a.Engine != nil {
		if err := a.Engine.Drain(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if a.Transactions != nil && a.shared == nil { // shared ones outlive the App
		a.Transactions.Close()
	}
	switch closer := a.Auditor.(type) {
	case interface{ CloseContext(context.Context) error }:
		if err := closer.CloseContext(ctx); err != nil {
			errs = append(errs, err)
		}
	case interface{ Close() }:
		closer.Close()
	}
	for _, prepared := range a.Prepared {
		if err := prepared.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if a.releaseConnections != nil {
		a.releaseConnections()
	} else {
		for _, provider := range a.allProviders() {
			if err := provider.Close(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if a.shared != nil {
		a.shared.cache.DropGeneration(a.generation)
	}
	a.closed = true
	return errors.Join(errs...)
}

// Close preserves the original unbounded close contract. Callers that need a
// shutdown deadline should use CloseContext.
func (a *App) Close() error {
	return a.CloseContext(context.Background())
}

// Ping verifies every configured database is reachable. It backs the
// /readyz/db readiness probe and stays off the tool execution path: it uses
// the provider's native connection ping when exposed and falls back to a
// trivial query otherwise.
func (a *App) Ping(ctx context.Context) error {
	for name, p := range a.namedProviders() {
		if err := pingProvider(ctx, p); err != nil {
			return fmt.Errorf("bootstrap: database %q not ready: %w", name, err)
		}
	}
	return nil
}

// poolExposer is a provider backed by a database/sql pool, which bootstrap
// sizes (configurePool) and pings natively (pingProvider).
type poolExposer interface{ DB() *sql.DB }

// namedProviders lists every distinct provider by datasource (and
// connection, when a datasource has several).
func (a *App) namedProviders() map[string]Provider {
	out := map[string]Provider{}
	seen := map[Provider]bool{}
	add := func(name string, p Provider) {
		if p == nil {
			return
		}
		if comparable := reflect.TypeOf(p).Comparable(); comparable && seen[p] {
			return
		} else if comparable {
			seen[p] = true
		}
		out[name] = p
	}
	for datasource, byName := range a.Connections {
		for connection, p := range byName {
			add(datasource+"/"+connection, p)
		}
	}
	for datasource, p := range a.Providers {
		add(datasource, p)
	}
	add("default", a.Provider)
	return out
}

func (a *App) allProviders() []Provider {
	out := make([]Provider, 0)
	for _, p := range a.namedProviders() {
		out = append(out, p)
	}
	return out
}

func pingProvider(ctx context.Context, p Provider) error {
	if native, ok := p.(poolExposer); ok {
		return native.DB().PingContext(ctx)
	}
	rows, err := p.QueryContext(ctx, "SELECT 1")
	if err != nil {
		return err
	}
	return rows.Close()
}

// Load reads and validates a YAML config file.
func Load(path string) (*config.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return LoadBytes(data)
}

// ValidateFile parses, defaults, validates, runs the offline assembly checks
// (CheckOffline) and resolves secrets without opening database connections.
func ValidateFile(path string, resolver SecretResolver) error {
	cfg, err := Load(path)
	if err != nil {
		return err
	}
	if err := CheckOffline(cfg); err != nil {
		return err
	}
	if resolver == nil {
		resolver = EnvFileResolver{AllowedRoots: cfg.Server.Secrets.AllowedRoots}
	}
	for name, database := range cfg.Databases {
		for connection, c := range database.ConnectionsOrDSN() {
			if _, err := resolver.Resolve(c.DSN); err != nil {
				return fmt.Errorf("database %q connection %q: %w", name, connection, err)
			}
		}
	}
	return nil
}

// Assemble opens the provider and wires the application from cfg, using the
// default EnvFileResolver for secret placeholders.
func Assemble(cfg *config.Config) (*App, error) {
	return AssembleWithResolver(cfg, EnvFileResolver{AllowedRoots: cfg.Server.Secrets.AllowedRoots})
}

// AssembleWithResolver resolves secrets with the given resolver, opens the
// provider, and wires the application. Use a custom resolver to integrate
// secret managers (Vault, AWS Secrets Manager, etc.) without coupling core to
// any specific backend.
func AssembleWithResolver(cfg *config.Config, r SecretResolver) (*App, error) {
	if len(cfg.Databases) == 0 {
		return nil, errors.New("assemble: no database configured")
	}
	connections, err := openConnections(cfg, r)
	if err != nil {
		return nil, err
	}
	app, err := AssembleWithConnections(cfg, connections)
	if err != nil {
		closeConnections(connections)
		return nil, err
	}
	app.scanResolver = r
	return app, nil
}

// OpenScan opens a connection of its own to the read connection of database,
// as given, for a schema scan: it takes no serving connection and does not
// hold the snapshot, so neither requests nor reloads wait for the scan, and it
// reads the database the caller names even before this App is replaced by
// one configured so. The caller closes it; ctx bounds opening it. ok is false
// when the App was assembled from connections it was handed and cannot open
// more.
func (a *App) OpenScan(ctx context.Context, database config.DatabaseConfig) (p Provider, ok bool, err error) {
	if a.scanResolver == nil {
		return nil, false, nil
	}
	read := database.Route().Read
	p, err = openConnection(ctx, a.config, a.scanResolver, database.Driver, database.ConnectionsOrDSN()[read])
	if err != nil {
		return nil, true, fmt.Errorf("connection %q: %w", read, err)
	}
	configurePool(p, 1, 0, 0)
	return p, true, nil
}

// openConnection opens connection c of a database with driver; ctx bounds
// opening it.
func openConnection(
	ctx context.Context, cfg *config.Config, r SecretResolver, driver string, c config.ConnectionConfig,
) (Provider, error) {
	dsn, err := r.Resolve(c.DSN)
	if err != nil {
		return nil, err
	}
	return openDSN(ctx, cfg, driver, dsn, c.Pooler, false)
}

// openDSN opens a connection to the resolved dsn; lazy opens it without
// reaching the database (see providerregistry.Options.Lazy).
func openDSN(ctx context.Context, cfg *config.Config, driver, dsn, pooler string, lazy bool) (Provider, error) {
	return providerregistry.New(driver, dsn,
		providerregistry.Options{Timeout: cfg.Cost.QueryTimeout, Pooler: pooler, Context: ctx, Lazy: lazy})
}

// openConnections opens every connection of every database.
func openConnections(cfg *config.Config, r SecretResolver) (Connections, error) {
	connections := make(Connections, len(cfg.Databases))
	for name, database := range cfg.Databases {
		connections[name] = map[string]Provider{}
		for connection, c := range database.ConnectionsOrDSN() {
			provider, err := openConnection(context.Background(), cfg, r, database.Driver, c)
			if err != nil {
				closeConnections(connections)
				return nil, fmt.Errorf("database %q connection %q: %w", name, connection, err)
			}
			connections[name][connection] = provider
		}
	}
	return connections, nil
}

// AssembleWithProvider wires the application using an injected provider (for
// testing with fakes).
func AssembleWithProvider(cfg *config.Config, prov Provider) (*App, error) {
	return AssembleWithProviders(cfg, map[string]Provider{"default": prov})
}

// AssembleWithProviders wires one provider per datasource, serving every
// connection the datasource configures. It is intended for tests and
// embedders; ownership transfers to the returned App on success.
func AssembleWithProviders(cfg *config.Config, providers map[string]Provider) (*App, error) {
	connections := make(Connections, len(providers))
	for datasource, p := range providers {
		connections[datasource] = map[string]Provider{}
		for connection := range cfg.Databases[datasource].ConnectionsOrDSN() {
			connections[datasource][connection] = p
		}
	}
	return AssembleWithConnections(cfg, connections)
}

// recordProviderFailure reports whether err tells of the database's health
// and so feeds the adaptive limit and the breaker. It counts only what the
// database or the connection to it reported (fail closed: an error of any
// other kind, such as a new business error, never opens the breaker); a
// cancelled or timed-out call is the caller's doing.
func recordProviderFailure(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var netErr net.Error
	return errors.Is(err, tool.ErrDatabase) || errors.Is(err, driver.ErrBadConn) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &netErr)
}

// Connections are opened providers by datasource and connection name.
type Connections map[string]map[string]Provider

func closeConnections(connections Connections) {
	for _, provider := range (&App{Connections: connections}).allProviders() {
		_ = provider.Close()
	}
}

// configurePool bounds the DB connection pool to the IO pool size so workers
// never wait on a connection they already hold a slot for.
func configurePool(p Provider, maxOpen int, connMaxIdle, connMaxLifetime time.Duration) {
	e, ok := p.(poolExposer)
	if !ok || maxOpen <= 0 {
		return
	}
	db := e.DB()
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxOpen)
	if connMaxIdle > 0 {
		db.SetConnMaxIdleTime(connMaxIdle)
	}
	if connMaxLifetime > 0 {
		db.SetConnMaxLifetime(connMaxLifetime)
	}
}

// reconcileEntities matches entities to their tables in the live schema and
// returns them with database comments as their default descriptions. An
// entity whose table or a configured column is missing from the database is
// marked unavailable (tools refuse it) instead of failing the configuration:
// a table dropped by its owners must not hold back every other change (extra
// DB columns are not drift).
func reconcileEntities(
	ctx context.Context,
	datasource string,
	prov Provider,
	entities []entity.Entity,
) ([]entity.Entity, error) {
	if prov.Introspector() == nil {
		return identifyRelations(entities, introspect.Physical{Server: "datasource:" + datasource}, nil), nil
	}
	schemas := make([]string, 0)
	unqualified := false
	for _, e := range entities {
		if e.Kind == entity.KindProcedure {
			continue
		}
		if e.Schema == "" {
			unqualified = true
		} else if !slices.Contains(schemas, e.Schema) {
			schemas = append(schemas, e.Schema)
		}
	}
	physical, err := physicalOf(ctx, datasource, prov.Introspector())
	if err != nil {
		return nil, fmt.Errorf("introspect: %w", err)
	}
	cat, err := introspect.LoadCatalog(ctx, prov.Introspector(), schemas, unqualified)
	if err != nil {
		return nil, fmt.Errorf("introspect: %w", err)
	}
	cat.FoldCase = physical.FoldCase
	reconciled, drift := introspect.Reconcile(entities, cat)
	markUnavailable(datasource, reconciled, drift.Absent)
	return identifyRelations(reconciled, physical, &cat), nil
}

// TableMissing is why an entity whose table is missing from the database is
// unavailable (see entity.Entity.Unavailable).
const TableMissing = "its table is missing from the database"

// Missing is what an App found missing from the databases, by entity ID:
// the entities whose tables are gone, and the fields and the other columns
// their row scopes test (see entity.Entity.RowColumns) that their tables
// lack. Entities of unreachable datasources are not known to miss anything.
type Missing struct {
	Tables             map[string]bool
	Fields, RowColumns map[string][]string
}

// Missing reports what the App found missing from the databases.
func (a *App) Missing() Missing {
	out := Missing{Tables: map[string]bool{}, Fields: map[string][]string{}, RowColumns: map[string][]string{}}
	if a.Registry == nil {
		return out
	}
	for _, e := range a.Registry.Entities() {
		why, ok := a.Unavailable[e.Name]
		if !ok {
			continue
		}
		if why == TableMissing {
			out.Tables[e.Name] = true
			continue
		}
		for _, c := range e.Absent {
			if slices.ContainsFunc(e.Attributes, func(a entity.Attribute) bool { return a.Name == c }) {
				out.Fields[e.Name] = append(out.Fields[e.Name], c)
			} else {
				out.RowColumns[e.Name] = append(out.RowColumns[e.Name], c)
			}
		}
	}
	return out
}

// markUnavailable marks the entities whose table or columns are absent.
func markUnavailable(datasource string, entities []entity.Entity, absent map[string][]string) {
	if len(absent) == 0 {
		return
	}
	for i := range entities {
		columns, ok := absent[entities[i].Name]
		switch {
		case !ok:
		case len(columns) == 0:
			entities[i].Unavailable = TableMissing
		default:
			entities[i].Unavailable = "columns missing from the database: " + strings.Join(columns, ", ")
			entities[i].Absent = columns
		}
	}
	slog.Warn("entities unavailable: missing from the database (remove them or restore the tables)",
		"datasource", datasource, "count", len(absent), "entities", slices.Sorted(maps.Keys(absent)))
}

var secretRe = regexp.MustCompile(`\$\{([^}]+)\}`)

// resolveSecretsWithRoots replaces ${ENV} and ${file:/path} placeholders. A
// missing env var or unreadable file fails fast rather than yielding an empty
// DSN.
func resolveSecretsWithRoots(s string, allowedRoots []string) (string, error) {
	var firstErr error
	out := secretRe.ReplaceAllStringFunc(s, func(m string) string {
		if firstErr != nil {
			return m
		}
		name := m[2 : len(m)-1]
		if strings.HasPrefix(name, "file:") {
			path, err := allowedSecretPath(name[len("file:"):], allowedRoots)
			if err != nil {
				firstErr = err
				return m
			}
			b, err := os.ReadFile(path)
			if err != nil {
				firstErr = fmt.Errorf("read secret file: %w", err)
				return m
			}
			return strings.TrimSpace(string(b))
		}
		v, ok := os.LookupEnv(name)
		if !ok {
			firstErr = fmt.Errorf("env %q not set", name)
			return m
		}
		return v
	})
	if firstErr != nil {
		return "", firstErr
	}
	return out, nil
}

func allowedSecretPath(path string, roots []string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("secret file path must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve secret file: %w", err)
	}
	for _, root := range roots {
		if !filepath.IsAbs(root) {
			continue
		}
		resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(resolvedRoot, resolved)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return resolved, nil
		}
	}
	return "", errors.New("secret file is outside allowed roots")
}

// SecretResolver resolves ${...} placeholders in a string (e.g. a DSN).
// EnvFileResolver is the built-in implementation; custom implementations can
// back this by Vault, AWS Secrets Manager, GCP Secret Manager, etc., without
// coupling core to any specific backend.
type SecretResolver interface {
	Resolve(s string) (string, error)
}

// EnvFileResolver resolves ${ENV} (environment variables) and ${file:/path}
// (file contents, e.g. Kubernetes Secret volume mounts).
type EnvFileResolver struct {
	AllowedRoots []string
}

// Resolve implements SecretResolver.
func (r EnvFileResolver) Resolve(s string) (string, error) {
	roots := r.AllowedRoots
	if len(roots) == 0 {
		roots = config.DefaultSecretRoots()
	}
	return resolveSecretsWithRoots(s, roots)
}

// validateMaskRules fails fast if a configured mask rule is unknown, so a typo
// never silently leaks plaintext at read time.
func validateMaskRules(m *mask.RuleMasker, entities []entity.Entity) error {
	for _, e := range entities {
		for _, a := range e.Attributes {
			if a.Mask != "" && !m.Has(a.Mask) {
				return fmt.Errorf("entity %q field %q: unknown mask rule %q", e.Name, a.Name, a.Mask)
			}
		}
	}
	return nil
}

func newBudgetManager(cfg *config.Config) *budget.MemoryManager {
	return budget.New(budgetLimits(cfg))
}

// budgetLimits are the role (see roleBudgets), user principal (see
// userBudgets) and tenant limits cfg sets.
func budgetLimits(cfg *config.Config) (map[string]budget.Limits, map[string]budget.Limits) {
	c, principals, byRole := cfg.Budget, userBudgets(cfg), roleBudgets(cfg)
	roles := make(map[string]budget.Limits, len(byRole)+len(principals))
	for name, limits := range byRole {
		roles[name] = budget.Limits(limits)
	}
	for principal, limits := range principals {
		roles[principal] = budget.Limits(limits)
	}
	tenants := make(map[string]budget.Limits, len(c.Tenants))
	for name, limits := range c.Tenants {
		tenants[name] = budget.Limits(limits)
	}
	return roles, tenants
}
