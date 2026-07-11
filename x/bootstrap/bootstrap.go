package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/nethinwei/sql-mcp-server/audit"
	"github.com/nethinwei/sql-mcp-server/budget"
	"github.com/nethinwei/sql-mcp-server/cache"
	"github.com/nethinwei/sql-mcp-server/config"
	"github.com/nethinwei/sql-mcp-server/cost"
	"github.com/nethinwei/sql-mcp-server/dialect"
	"github.com/nethinwei/sql-mcp-server/engine"
	"github.com/nethinwei/sql-mcp-server/entity"
	"github.com/nethinwei/sql-mcp-server/hook"
	"github.com/nethinwei/sql-mcp-server/introspect"
	"github.com/nethinwei/sql-mcp-server/mask"
	"github.com/nethinwei/sql-mcp-server/ratelimit"
	"github.com/nethinwei/sql-mcp-server/rbac"
	"github.com/nethinwei/sql-mcp-server/relalg"
	"github.com/nethinwei/sql-mcp-server/store"
	"github.com/nethinwei/sql-mcp-server/tool"
	"github.com/nethinwei/sql-mcp-server/x/providers/mysql"
	"github.com/nethinwei/sql-mcp-server/x/providers/oceanbase"
	"github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

// ErrUnsupportedDriver is returned for a driver with no provider yet.
var ErrUnsupportedDriver = fmt.Errorf("bootstrap: unsupported driver")

// Provider aggregates the core interfaces a database adapter must satisfy.
type Provider interface {
	store.DB
	store.TxBeginner
	Dialect() dialect.Dialect
	Explainer() cost.Explainer
	Introspector() introspect.Introspector
	Close() error
}

// App is the assembled application, ready to serve.
type App struct {
	Provider     Provider
	Providers    map[string]Provider
	Prepared     map[string]*store.PreparedDB
	Sources      map[string]tool.DataSource
	Dialect      dialect.Dialect
	Registry     *entity.Registry
	Authorizer   rbac.Authorizer
	Masker       mask.Masker
	Gate         cost.Gate
	Engine       *engine.Engine
	Tools        *tool.Registry
	ToolFlags    config.ToolFlags
	DefaultRole  string
	QueryTimeout time.Duration
	Auditor      audit.Auditor
	Hooks        *hook.Hooks
	Cache        cache.Cache[[]map[string]any]
	Feedback     cost.FeedbackStore
	Analyze      cost.AnalyzePolicy
	Budget       budget.Manager
	Transactions *tool.TransactionManager
	TxBeginners  map[string]store.TxBeginner
	closeMu      sync.Mutex
	closed       bool
}

// ToolContext builds a per-request tool.Context for the given role.
func (a *App) ToolContext(role string) tool.Context {
	return tool.Context{
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
		Timeout:      a.QueryTimeout,
		Feedback:     a.Feedback,
		Analyze:      a.Analyze,
		Sources:      a.Sources,
		Budget:       a.Budget,
		Transactions: a.Transactions,
		TxBeginners:  a.TxBeginners,
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
	if a.Engine != nil {
		if err := a.Engine.Drain(ctx); err != nil {
			return err
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
	var first error
	for _, prepared := range a.Prepared {
		if err := prepared.Close(); err != nil && first == nil {
			first = err
		}
	}
	for _, provider := range a.Providers {
		if err := provider.Close(); err != nil && first == nil {
			first = err
		}
	}
	if len(a.Providers) == 0 && a.Provider != nil {
		first = a.Provider.Close()
	}
	a.closed = true
	return first
}

// Close preserves the original unbounded close contract. Callers that need a
// shutdown deadline should use CloseContext.
func (a *App) Close() error {
	return a.CloseContext(context.Background())
}

// Load reads and validates a YAML config file.
func Load(path string) (*config.Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg config.Config
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// ValidateFile parses, defaults, validates, and resolves secrets without
// opening database connections.
func ValidateFile(path string, resolver SecretResolver) error {
	cfg, err := Load(path)
	if err != nil {
		return err
	}
	if resolver == nil {
		resolver = EnvFileResolver{}
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
	return AssembleWithResolver(cfg, EnvFileResolver{})
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
		provider, err := newProvider(database.Driver, dsn)
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

// AssembleWithProviders wires named providers. It is intended for tests and
// embedders; ownership transfers to the returned App on success.
func AssembleWithProviders(cfg *config.Config, providers map[string]Provider) (*App, error) {
	datasources := make([]string, 0, len(providers))
	for name := range providers {
		datasources = append(datasources, name)
	}
	if err := cost.ValidateTemplateScopes(datasources, cfg.Cost.AllowTemplates, cfg.Cost.RejectTemplates); err != nil {
		return nil, fmt.Errorf("assemble: %w", err)
	}
	for _, prov := range providers {
		configurePool(prov, cfg.RateLimit.IOPool, cfg.RateLimit.ConnMaxIdleTime)
	}
	entities, err := configToEntities(cfg.Entities)
	if err != nil {
		return nil, err
	}
	for _, e := range entities {
		if _, ok := providers[e.DataSource]; !ok {
			return nil, fmt.Errorf("entity %q references unavailable datasource %q", e.Name, e.DataSource)
		}
	}
	for name, prov := range providers {
		var scoped []entity.Entity
		for _, e := range entities {
			if e.DataSource == name {
				scoped = append(scoped, e)
			}
		}
		if err := checkDrift(context.Background(), prov, scoped); err != nil {
			return nil, fmt.Errorf("datasource %q: %w", name, err)
		}
	}
	reg, err := entity.NewRegistry(entities)
	if err != nil {
		return nil, err
	}
	auth := rbac.NewRoleAuthorizer(reg)
	var feedback cost.FeedbackStore = cost.NoopFeedbackStore{}
	if cfg.Cost.EnabledOrDefault() {
		feedback = cost.NewAdaptiveMemoryStore(
			cfg.Cost.AQE.WindowSize,
			cfg.Cost.AQE.AnomalyFactor,
			cfg.Cost.AQE.AnomalyMinSamples,
			nil,
		)
	}
	sources := make(map[string]tool.DataSource, len(providers))
	txBeginners := make(map[string]store.TxBeginner, len(providers))
	prepared := make(map[string]*store.PreparedDB, len(providers))
	for name, prov := range providers {
		txBeginners[name] = prov
		db := store.WithPreparedCache(prov, cfg.Cache.PreparedMaxSize)
		prepared[name] = db
		sampler, supportsAnalyze := prov.(cost.AnalyzeSampler)
		if cfg.Cost.AQE.ExplainAnalyze && !supportsAnalyze {
			return nil, fmt.Errorf("datasource %q (%s) does not support EXPLAIN ANALYZE sampling", name, prov.Dialect().Name())
		}
		analyze := cost.AnalyzePolicy{
			Sampler: sampler,
			Config: cost.AnalyzeConfig{
				Enabled:    cfg.Cost.AQE.ExplainAnalyze,
				ReadOnly:   cfg.Cost.AQE.ReadOnly,
				SampleRate: cfg.Cost.AQE.SampleRate,
				Timeout:    cfg.Cost.AQE.Timeout,
			},
		}
		var gate cost.Gate
		if cfg.Cost.EnabledOrDefault() {
			threshold := toThreshold(cfg.Cost)
			threshold.Datasource = name
			threshold.DialectName = prov.Dialect().Name()
			threshold.LegacyExactSQL = len(providers) == 1
			gate = cost.NewGateFromCapabilities(prov.Dialect().Capabilities(), prov.Explainer(), threshold, feedback)
		}
		sources[name] = tool.DataSource{DB: db, Dialect: prov.Dialect(), Gate: gate, Analyze: analyze}
	}
	var limiter *ratelimit.Adaptive
	var breaker *ratelimit.Breaker
	if cfg.RateLimit.EnabledOrDefault() {
		limiter = ratelimit.NewAdaptive(int64(cfg.RateLimit.IOPool), int64(cfg.RateLimit.MinConcurrency), int64(cfg.RateLimit.MaxInflight), cfg.RateLimit.RTTThreshold)
		breaker = ratelimit.NewBreaker(int64(cfg.RateLimit.BreakerThreshold), cfg.RateLimit.BreakerCooldown)
	}
	eng, err := engine.New(
		engine.WithIOPool(cfg.RateLimit.IOPool),
		engine.WithCPUPool(cfg.RateLimit.CPUPool),
		engine.WithMaxInflight(cfg.RateLimit.MaxInflight),
		engine.WithLimiter(limiter),
		engine.WithBreaker(breaker),
		engine.WithFailureClassifier(recordProviderFailure),
	)
	if err != nil {
		return nil, err
	}
	tools, err := tool.NewRegistry(tool.DefaultTools())
	if err != nil {
		return nil, err
	}
	var aud audit.Auditor = audit.NoopAuditor{}
	if cfg.Audit.Enabled && cfg.Audit.Path != "" {
		aud = audit.NewAsyncAuditor(fileSink(cfg.Audit.Path), cfg.Audit.QueueSize)
	}
	var cc cache.Cache[[]map[string]any] = cache.NoopCache[[]map[string]any]{}
	if cfg.Cache.Enabled {
		cc = cache.NewTTLCache[[]map[string]any](cfg.Cache.TTL, cfg.Cache.MaxSize)
	}
	var msk mask.Masker = mask.NoopMasker{}
	if cfg.Mask.EnabledOrDefault() {
		rm := mask.NewRuleMasker(nil)
		if err := validateMaskRules(rm, entities); err != nil {
			return nil, err
		}
		msk = rm
	}
	defaultName := "default"
	if _, ok := providers[defaultName]; !ok && len(providers) == 1 {
		for name := range providers {
			defaultName = name
		}
	}
	defaultProvider := providers[defaultName]
	defaultSource := sources[defaultName]
	return &App{
		Provider:     defaultProvider,
		Providers:    providers,
		Prepared:     prepared,
		Sources:      sources,
		Dialect:      defaultSource.Dialect,
		Registry:     reg,
		Authorizer:   auth,
		Masker:       msk,
		Feedback:     feedback,
		Analyze:      defaultSource.Analyze,
		Gate:         defaultSource.Gate,
		Engine:       eng,
		Tools:        tools,
		ToolFlags:    cfg.Tools,
		DefaultRole:  cfg.Server.Role,
		QueryTimeout: cfg.Cost.QueryTimeout,
		Auditor:      aud,
		Cache:        cc,
		Budget:       newBudgetManager(cfg.Budget),
		Transactions: tool.NewTransactionManager(cfg.Transactions.TTL, cfg.Transactions.MaxOpen),
		TxBeginners:  txBeginners,
	}, nil
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

func newProvider(driver, dsn string) (Provider, error) {
	switch driver {
	case "postgres":
		return postgres.New(dsn)
	case "mysql":
		return mysql.New(dsn)
	case "oceanbase":
		return oceanbase.New(dsn)
	}
	return nil, fmt.Errorf("%w: %q", ErrUnsupportedDriver, driver)
}

// configurePool bounds the DB connection pool to the IO pool size so workers
// never wait on a connection they already hold a slot for.
func configurePool(p Provider, maxOpen int, connMaxIdle time.Duration) {
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
		Enabled:           c.EnabledOrDefault(),
		SoftScore:         c.SoftScore,
		HardScore:         c.HardScore,
		MaxRows:           c.MaxRows,
		MaxBytes:          c.MaxBytes,
		RejectFullScan:    c.RejectFullScan,
		WhitelistPKPoint:  c.WhitelistPKPoint,
		RequirePKForWrite: c.RequirePKForWriteOrDefault(),
		RequireKnownScan:  c.RequireKnownScan,
		RequireFreshStats: c.RequireFreshStats,
		AllowTemplates:    c.AllowTemplates,
		RejectTemplates:   c.RejectTemplates,
	}
}

// resolveSecrets replaces ${ENV} and ${file:/path} placeholders. A missing env
// var or unreadable file fails fast rather than yielding an empty DSN.
var secretRe = regexp.MustCompile(`\$\{([^}]+)\}`)

func resolveSecrets(s string) (string, error) {
	var firstErr error
	out := secretRe.ReplaceAllStringFunc(s, func(m string) string {
		if firstErr != nil {
			return m
		}
		name := m[2 : len(m)-1]
		if strings.HasPrefix(name, "file:") {
			b, err := os.ReadFile(name[len("file:"):])
			if err != nil {
				firstErr = fmt.Errorf("read secret file %q: %w", name, err)
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

func fileSink(path string) audit.Sink {
	return func(e audit.Event) error {
		b, err := yaml.Marshal(e)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		_, err = f.Write(b)
		return err
	}
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
type EnvFileResolver struct{}

// Resolve implements SecretResolver.
func (EnvFileResolver) Resolve(s string) (string, error) { return resolveSecrets(s) }

var (
	pgPassRe    = regexp.MustCompile(`(://[^:/@]+:)[^@]+(@)`)
	mysqlPassRe = regexp.MustCompile(`^([^:@]+:)[^@]+(@tcp)`)
)

// RedactDSN returns dsn with any password replaced by ***, for safe logging.
// It handles PostgreSQL URI form (scheme://user:pass@host) and MySQL DSN form
// (user:pass@tcp(host)); DSNs without a password are returned unchanged.
func RedactDSN(dsn string) string {
	if pgPassRe.MatchString(dsn) {
		return pgPassRe.ReplaceAllString(dsn, "${1}***${2}")
	}
	return mysqlPassRe.ReplaceAllString(dsn, "${1}***${2}")
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

// configToEntities converts config entities to the core entity model.
func configToEntities(ecs []config.EntityConfig) ([]entity.Entity, error) {
	out := make([]entity.Entity, 0, len(ecs))
	for _, ec := range ecs {
		e, err := configToEntity(ec)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func configToEntity(ec config.EntityConfig) (entity.Entity, error) {
	source := ec.Source
	if source == "" {
		source = ec.Name
	}
	dataSource := ec.DataSource
	if dataSource == "" {
		dataSource = "default"
	}
	attrs := make([]entity.Attribute, 0, len(ec.Fields))
	for _, f := range ec.Fields {
		attrs = append(attrs, entity.Attribute{
			Name: f.Name, Alias: f.Alias, Description: f.Description,
			Mask: f.Mask, Excluded: f.Exclude,
		})
	}
	role := entity.RoleAccess{
		entity.ActionRead:      ec.Roles.Read,
		entity.ActionCreate:    ec.Roles.Create,
		entity.ActionUpdate:    ec.Roles.Update,
		entity.ActionDelete:    ec.Roles.Delete,
		entity.ActionExecute:   ec.Roles.Execute,
		entity.ActionAggregate: ec.Roles.Aggregate,
	}
	fieldAccess := make(entity.FieldAccess, len(ec.FieldACL))
	for role, acl := range ec.FieldACL {
		fieldAccess[role] = entity.FieldPermissions{Read: acl.Read, Write: acl.Write}
	}
	rowPolicies := entity.RowPolicies{}
	for r, fc := range ec.RowPolicies {
		p, err := filterConfigToPredicate(fc)
		if err != nil {
			return entity.Entity{}, fmt.Errorf("row policy for role %q: %w", r, err)
		}
		rowPolicies[r] = p
	}
	var keys []entity.Key
	if len(ec.PrimaryKey) > 0 {
		keys = []entity.Key{{Name: "pk", Columns: ec.PrimaryKey, Primary: true}}
	}
	relations := make([]entity.Relationship, 0, len(ec.Relationships))
	for _, relation := range ec.Relationships {
		relations = append(relations, entity.Relationship{
			Name: relation.Name, Target: relation.Target,
			Cardinality: relation.Cardinality, JoinOn: relation.JoinOn,
		})
	}
	return entity.Entity{
		Name: ec.Name, Source: source, DataSource: dataSource, Schema: ec.Schema, Description: ec.Description,
		Kind: parseKind(ec.Kind), Attributes: attrs, Keys: keys, Role: role, FieldAccess: fieldAccess,
		MCP:         entity.MCPFlags{DMLTools: ec.MCP.DMLTools, CustomTool: ec.MCP.CustomTool},
		RowPolicies: rowPolicies,
		Relations:   relations,
		Params:      ec.Params,
	}, nil
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
	return budget.Limits{
		MaxConcurrent: c.MaxConcurrent, MaxExecution: c.MaxExecution,
		MaxScannedRows: c.MaxScannedRows, MaxReturnedRows: c.MaxReturnedRows,
		MaxSessionCost: c.MaxSessionCost,
	}
}

func parseKind(s string) entity.Kind {
	switch s {
	case "view":
		return entity.KindView
	case "procedure":
		return entity.KindProcedure
	}
	return entity.KindTable
}

// filterConfigToPredicate converts a declarative filter config to a relalg
// predicate. Supported shapes: {op,field,value}, {and:[...]}, {or:[...]}.
func filterConfigToPredicate(fc config.FilterConfig) (relalg.Predicate, error) {
	if fc == nil {
		return nil, nil
	}
	if op, ok := fc["op"].(string); ok {
		field, _ := fc["field"].(string)
		return relalg.Condition{Field: field, Op: relalg.Op(op), Value: fc["value"]}, nil
	}
	if and, ok := fc["and"].([]any); ok {
		return combineFilters(and, relalg.And{})
	}
	if or, ok := fc["or"].([]any); ok {
		return combineFilters(or, relalg.Or{})
	}
	return nil, fmt.Errorf("invalid filter config: missing op/and/or")
}

func combineFilters(items []any, wrap relalg.Predicate) (relalg.Predicate, error) {
	preds := make([]relalg.Predicate, 0, len(items))
	for _, item := range items {
		m, ok := item.(config.FilterConfig)
		if !ok {
			return nil, fmt.Errorf("filter item is not an object")
		}
		p, err := filterConfigToPredicate(m)
		if err != nil {
			return nil, err
		}
		preds = append(preds, p)
	}
	switch w := wrap.(type) {
	case relalg.And:
		w.Preds = preds
		return w, nil
	case relalg.Or:
		w.Preds = preds
		return w, nil
	}
	return nil, fmt.Errorf("unsupported combiner")
}

// context.Background import guard removed (unused here).
