package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/audit"
	"github.com/nethinwei/sql-mcp-server/core/budget"
	"github.com/nethinwei/sql-mcp-server/core/cache"
	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/core/engine"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/hook"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/core/mask"
	coreprovider "github.com/nethinwei/sql-mcp-server/core/provider"
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
	Provider    Provider
	Providers   map[string]Provider
	Prepared    map[string]*store.PreparedDB
	Sources     map[string]tool.DataSource
	Dialect     dialect.Dialect
	Registry    *entity.Registry
	Authorizer  rbac.Authorizer
	Masker      mask.Masker
	Gate        cost.Gate
	Engine      *engine.Engine
	Tools       *tool.Registry
	ToolFlags   config.ToolFlags
	DefaultRole string
	DefaultUser string
	// Users maps an enabled user name to its identity; UserTokens maps a
	// tokenHash to the user name. Both follow the snapshot on reload.
	Users        map[string]UserIdentity
	UserTokens   map[string]string
	Limits       tool.Limits
	Auditor      audit.Auditor
	Hooks        *hook.Hooks
	Cache        cache.Cache[[]map[string]any]
	Feedback     cost.FeedbackStore
	Analyze      cost.AnalyzePolicy
	Budget       *budget.MemoryManager
	Transactions *tool.TransactionManager
	TxBeginners  map[string]store.TxBeginner
	closeMu      sync.Mutex
	closed       bool
}

// ToolContext builds a per-request tool.Context for the given role.
func (a *App) ToolContext(role string) tool.Context {
	tc := tool.Context{
		Role:         role,
		DB:           a.Provider,
		Dialect:      a.Dialect,
		Registry:     a.Registry,
		Authorizer:   a.Authorizer,
		Masker:       a.Masker,
		Gate:         a.Gate,
		Cache:        a.Cache,
		Engine:       a.Engine,
		Auditor:      a.Auditor,
		Hooks:        a.Hooks,
		Limits:       a.Limits,
		Feedback:     a.Feedback,
		Analyze:      a.Analyze,
		Sources:      a.Sources,
		Transactions: a.Transactions,
		TxBeginners:  a.TxBeginners,
	}
	if a.Budget != nil { // a nil *MemoryManager must stay a nil interface
		tc.Budget = a.Budget
	}
	return tc
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

// CloseContext stops engine admission and drains all execution before rolling
// back transactions and releasing audit/provider resources.
func (a *App) CloseContext(ctx context.Context) error {
	a.closeMu.Lock()
	defer a.closeMu.Unlock()
	if a.closed {
		return nil
	}
	var errs []error
	if a.Engine != nil {
		if err := a.Engine.Drain(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if a.Transactions != nil {
		a.Transactions.Close()
	}
	if a.Auditor != nil {
		if closer, ok := a.Auditor.(interface{ Close() }); ok {
			closer.Close()
		}
	}
	for _, prepared := range a.Prepared {
		if err := prepared.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	for _, provider := range a.Providers {
		if err := provider.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if len(a.Providers) == 0 && a.Provider != nil {
		if err := a.Provider.Close(); err != nil {
			errs = append(errs, err)
		}
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
	providers := a.Providers
	if len(providers) == 0 && a.Provider != nil {
		providers = map[string]Provider{"default": a.Provider}
	}
	for name, p := range providers {
		if err := pingProvider(ctx, p); err != nil {
			return fmt.Errorf("bootstrap: database %q not ready: %w", name, err)
		}
	}
	return nil
}

// poolExposer is a provider backed by a database/sql pool, which bootstrap
// sizes (configurePool) and pings natively (pingProvider).
type poolExposer interface{ DB() *sql.DB }

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

// ValidateFile parses, defaults, validates, and resolves secrets without
// opening database connections.
func ValidateFile(path string, resolver SecretResolver) error {
	cfg, err := Load(path)
	if err != nil {
		return err
	}
	if resolver == nil {
		resolver = EnvFileResolver{AllowedRoots: cfg.Server.Secrets.AllowedRoots}
	}
	for name, database := range cfg.Databases {
		if _, err := resolver.Resolve(database.DSN); err != nil {
			return fmt.Errorf("database %q: %w", name, err)
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
	providers := make(map[string]Provider, len(cfg.Databases))
	for name, database := range cfg.Databases {
		dsn, err := r.Resolve(database.DSN)
		if err != nil {
			closeProviders(providers)
			return nil, err
		}
		provider, err := providerregistry.New(database.Driver, dsn, cfg.Cost.QueryTimeout)
		if err != nil {
			closeProviders(providers)
			return nil, err
		}
		providers[name] = provider
	}
	app, err := AssembleWithProviders(cfg, providers)
	if err != nil {
		closeProviders(providers)
		return nil, err
	}
	return app, nil
}

// AssembleWithProvider wires the application using an injected provider (for
// testing with fakes).
func AssembleWithProvider(cfg *config.Config, prov Provider) (*App, error) {
	return AssembleWithProviders(cfg, map[string]Provider{"default": prov})
}

func recordProviderFailure(err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, budget.ErrExceeded), errors.Is(err, cost.ErrCostExceeded),
		errors.Is(err, tool.ErrUnauthorized), errors.Is(err, tool.ErrEntityNotFound),
		errors.Is(err, tool.ErrInvalidInput), errors.Is(err, tool.ErrDMLToolsDisabled),
		errors.Is(err, tool.ErrUnsafeWrite),
		errors.Is(err, tool.ErrTransactionNotFound), errors.Is(err, tool.ErrTransactionScope),
		errors.Is(err, tool.ErrTransactionCapacity):
		return false
	default:
		return true
	}
}

func closeProviders(providers map[string]Provider) {
	for _, provider := range providers {
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

// reconcileEntities matches entities to their tables in the live schema. It
// fails fast if a configured entity or field is missing from the database
// (extra DB columns are not fatal) and returns the entities with database
// comments as their default descriptions.
func reconcileEntities(ctx context.Context, prov Provider, entities []entity.Entity) ([]entity.Entity, error) {
	if prov.Introspector() == nil {
		return entities, nil
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
	cat, err := introspect.LoadCatalog(ctx, prov.Introspector(), schemas, unqualified)
	if err != nil {
		return nil, fmt.Errorf("introspect: %w", err)
	}
	reconciled, drift := introspect.Reconcile(entities, cat)
	if len(drift.Missing) > 0 {
		return nil, fmt.Errorf("schema drift (configured but missing in DB): %v", drift.Missing)
	}
	return reconciled, nil
}

// toThreshold maps config.CostConfig to cost.Threshold.
func toThreshold(c config.CostConfig) cost.Threshold {
	return cost.Threshold{
		SoftScore:                 c.SoftScore,
		HardScore:                 c.HardScore,
		MaxRows:                   c.MaxRows,
		MaxBytes:                  c.MaxBytes,
		RejectFullScan:            c.RejectFullScan,
		WhitelistPKPoint:          c.WhitelistPKPoint,
		RequirePKForWrite:         c.RequirePKForWriteOrDefault(),
		RequireAggregatePredicate: true,
		ExplainFailClosed:         c.EnabledOrDefault(),
		RequireKnownScan:          c.RequireKnownScan,
		RequireFreshStats:         c.RequireFreshStats,
		AllowTemplates:            c.AllowTemplates,
		RejectTemplates:           c.RejectTemplates,
	}
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

func newBudgetManager(c config.BudgetConfig, principals map[string]config.BudgetLimits) *budget.MemoryManager {
	roles := make(map[string]budget.Limits, len(c.Roles)+len(principals))
	for name, limits := range c.Roles {
		roles[name] = toBudgetLimits(limits)
	}
	for principal, limits := range principals {
		roles[principal] = toBudgetLimits(limits)
	}
	tenants := make(map[string]budget.Limits, len(c.Tenants))
	for name, limits := range c.Tenants {
		tenants[name] = toBudgetLimits(limits)
	}
	return budget.New(roles, tenants)
}

func toBudgetLimits(c config.BudgetLimits) budget.Limits {
	return budget.Limits{
		MaxConcurrent: c.MaxConcurrent, MaxExecution: c.MaxExecution,
		MaxEstimatedScannedRows: c.MaxEstimatedScannedRows, MaxReturnedRows: c.MaxReturnedRows,
		MaxReturnedBytes: c.MaxReturnedBytes, MaxSessionCost: c.MaxSessionCost,
	}
}
