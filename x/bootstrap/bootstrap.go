package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
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
	"github.com/nethinwei/sql-mcp-server/x/configyaml"
	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
)

// ErrUnsupportedDriver is returned for a driver with no provider yet.
var ErrUnsupportedDriver = providerregistry.ErrUnsupportedDriver

// Provider aggregates the core interfaces a database adapter must satisfy.
type Provider = coreprovider.Provider

// App is the assembled application, ready to serve.
type App struct {
	Provider                   Provider
	Providers                  map[string]Provider
	Prepared                   map[string]*store.PreparedDB
	Sources                    map[string]tool.DataSource
	Dialect                    dialect.Dialect
	Registry                   *entity.Registry
	Authorizer                 rbac.Authorizer
	Masker                     mask.Masker
	Gate                       cost.Gate
	Engine                     *engine.Engine
	Tools                      *tool.Registry
	ToolFlags                  config.ToolFlags
	DefaultRole                string
	QueryTimeout               time.Duration
	MaxRows                    int64
	MaxProcedureRows           int64
	MaxReturnedBytes           int64
	MaxINListSize              int
	MaxFilterConditions        int
	MaxGroupByFields           int
	MaxAggregates              int
	MaxExpand                  int
	CacheMaxEntryRows          int
	CacheMaxEntryBytes         int64
	TransactionBeginTimeout    time.Duration
	TransactionCommitTimeout   time.Duration
	TransactionRollbackTimeout time.Duration
	Auditor                    audit.Auditor
	Hooks                      *hook.Hooks
	Cache                      cache.Cache[[]map[string]any]
	Feedback                   cost.FeedbackStore
	Analyze                    cost.AnalyzePolicy
	Budget                     budget.Manager
	Transactions               *tool.TransactionManager
	TxBeginners                map[string]store.TxBeginner
	closeMu                    sync.Mutex
	closed                     bool
}

// ToolContext builds a per-request tool.Context for the given role.
func (a *App) ToolContext(role string) tool.Context {
	return tool.Context{
		Role:                       role,
		DB:                         a.Provider,
		Dialect:                    a.Dialect,
		Registry:                   a.Registry,
		Authorizer:                 a.Authorizer,
		Masker:                     a.Masker,
		Gate:                       a.Gate,
		Cache:                      a.Cache,
		Engine:                     a.Engine,
		Auditor:                    a.Auditor,
		Hooks:                      a.Hooks,
		Timeout:                    a.QueryTimeout,
		MaxRows:                    a.MaxRows,
		MaxProcedureRows:           a.MaxProcedureRows,
		MaxReturnedBytes:           a.MaxReturnedBytes,
		MaxINListSize:              a.MaxINListSize,
		MaxFilterConditions:        a.MaxFilterConditions,
		MaxGroupByFields:           a.MaxGroupByFields,
		MaxAggregates:              a.MaxAggregates,
		MaxExpand:                  a.MaxExpand,
		CacheMaxEntryRows:          a.CacheMaxEntryRows,
		CacheMaxEntryBytes:         a.CacheMaxEntryBytes,
		TransactionBeginTimeout:    a.TransactionBeginTimeout,
		TransactionCommitTimeout:   a.TransactionCommitTimeout,
		TransactionRollbackTimeout: a.TransactionRollbackTimeout,
		Feedback:                   a.Feedback,
		Analyze:                    a.Analyze,
		Sources:                    a.Sources,
		Budget:                     a.Budget,
		Transactions:               a.Transactions,
		TxBeginners:                a.TxBeginners,
	}
}

// ToolContextForSubject builds a per-request tool.Context for a role plus
// subject attributes (referenced by row-level ${subject.x} policies).
func (a *App) ToolContextForSubject(role string, subject map[string]any) tool.Context {
	tc := a.ToolContext(role)
	tc.Subject = subject
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

// Load reads and validates a YAML config file.
func Load(path string) (*config.Config, error) {
	cfg, err := configyaml.Load(path)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	for name, database := range cfg.Databases {
		if !providerregistry.IsRegistered(database.Driver) {
			return nil, fmt.Errorf("%w: %q in database %q", ErrUnsupportedDriver, database.Driver, name)
		}
	}
	return cfg, nil
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
	databases := cfg.Databases
	if len(databases) == 0 {
		databases = map[string]config.DatabaseConfig{"default": cfg.Database}
	}
	for name, database := range databases {
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
	databases := cfg.Databases
	if len(databases) == 0 {
		databases = map[string]config.DatabaseConfig{"default": cfg.Database}
	}
	providers := make(map[string]Provider, len(databases))
	for name, database := range databases {
		dsn, err := r.Resolve(database.DSN)
		if err != nil {
			closeProviders(providers)
			return nil, err
		}
		provider, err := newProvider(database.Driver, dsn, cfg.Cost.QueryTimeout)
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
		errors.Is(err, tool.ErrUnsafeWrite), errors.Is(err, tool.ErrNotImplemented),
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

func newProvider(driver, dsn string, timeout time.Duration) (Provider, error) {
	return providerregistry.New(driver, dsn, timeout)
}

// configurePool bounds the DB connection pool to the IO pool size so workers
// never wait on a connection they already hold a slot for.
func configurePool(p Provider, maxOpen int, connMaxIdle, connMaxLifetime time.Duration) {
	type dbExposer interface{ DB() *sql.DB }
	e, ok := p.(dbExposer)
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

// checkDrift introspects the live schema and fails fast if a configured entity
// or field is missing from the database. Extra DB columns are not fatal.
func checkDrift(ctx context.Context, prov Provider, entities []entity.Entity) error {
	if prov.Introspector() == nil {
		return nil
	}
	schemas := make([]string, 0)
	seen := make(map[string]bool)
	for _, e := range entities {
		if e.Schema != "" && !seen[e.Schema] {
			seen[e.Schema] = true
			schemas = append(schemas, e.Schema)
		}
	}
	discovered, err := prov.Introspector().Discover(ctx, schemas)
	if err != nil {
		return fmt.Errorf("introspect: %w", err)
	}
	// Procedures are not discovered as base tables; check drift only for
	// table/view entities.
	var tables []entity.Entity
	for _, e := range entities {
		if e.Kind != entity.KindProcedure {
			tables = append(tables, e)
		}
	}
	drift := introspect.DetectDrift(tables, discovered)
	if len(drift.Missing) > 0 {
		return fmt.Errorf("schema drift (configured but missing in DB): %v", drift.Missing)
	}
	return nil
}

// toThreshold maps config.CostConfig to cost.Threshold.
func toThreshold(c config.CostConfig) cost.Threshold {
	return cost.Threshold{
		Enabled:                   c.EnabledOrDefault(),
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

// resolveSecrets replaces ${ENV} and ${file:/path} placeholders. A missing env
// var or unreadable file fails fast rather than yielding an empty DSN.
var secretRe = regexp.MustCompile(`\$\{([^}]+)\}`)

func resolveSecrets(s string) (string, error) {
	return resolveSecretsWithRoots(s, []string{"/run/secrets", "/var/run/secrets"})
}

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
		roots = []string{"/run/secrets", "/var/run/secrets"}
	}
	return resolveSecretsWithRoots(s, roots)
}

var (
	pgPassRe    = regexp.MustCompile(`(://[^:/@]+:)[^@]+(@)`)
	mysqlPassRe = regexp.MustCompile(`^([^:@]+:)[^@]+(@tcp)`)
	keyPassRe   = regexp.MustCompile(`(?i)(^|[?;&\s])(password|pwd)=([^&;\s]+)`)
)

// RedactDSN returns dsn with any password replaced by ***, for safe logging.
// It handles PostgreSQL URI form (scheme://user:pass@host) and MySQL DSN form
// (user:pass@tcp(host)); DSNs without a password are returned unchanged.
func RedactDSN(dsn string) string {
	redacted := dsn
	if parsed, err := url.Parse(dsn); err == nil && parsed.Scheme != "" {
		if parsed.User != nil {
			if _, hasPassword := parsed.User.Password(); hasPassword {
				parsed.User = url.UserPassword(parsed.User.Username(), "***")
			}
		}
		query := parsed.Query()
		for _, key := range []string{"password", "pwd"} {
			if query.Has(key) {
				query.Set(key, "***")
			}
		}
		parsed.RawQuery = query.Encode()
		redacted = parsed.String()
	} else if pgPassRe.MatchString(redacted) {
		redacted = pgPassRe.ReplaceAllString(redacted, "${1}***${2}")
	}
	redacted = mysqlPassRe.ReplaceAllString(redacted, "${1}***${2}")
	return keyPassRe.ReplaceAllString(redacted, "${1}${2}=***")
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

func newBudgetManager(c config.BudgetConfig) budget.Manager {
	roles := make(map[string]budget.Limits, len(c.Roles))
	for name, limits := range c.Roles {
		roles[name] = toBudgetLimits(limits)
	}
	tenants := make(map[string]budget.Limits, len(c.Tenants))
	for name, limits := range c.Tenants {
		tenants[name] = toBudgetLimits(limits)
	}
	return budget.New(roles, tenants)
}

func toBudgetLimits(c config.BudgetLimits) budget.Limits {
	maxEstimated := c.MaxEstimatedScannedRows
	if maxEstimated == 0 {
		maxEstimated = c.MaxScannedRows
	}
	return budget.Limits{
		MaxConcurrent: c.MaxConcurrent, MaxExecution: c.MaxExecution,
		MaxEstimatedScannedRows: maxEstimated, MaxReturnedRows: c.MaxReturnedRows,
		MaxReturnedBytes: c.MaxReturnedBytes, MaxSessionCost: c.MaxSessionCost,
	}
}
