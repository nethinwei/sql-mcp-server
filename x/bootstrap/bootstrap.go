package bootstrap

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/nethinwei/sql-mcp-server/audit"
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
}

// ToolContext builds a per-request tool.Context for the given role.
func (a *App) ToolContext(role string) tool.Context {
	return tool.Context{
		Role:       role,
		DB:         a.Provider,
		Dialect:    a.Dialect,
		Registry:   a.Registry,
		Authorizer: a.Authorizer,
		Masker:     a.Masker,
		Gate:       a.Gate,
		Cache:      a.Cache,
		Engine:     a.Engine,
		Hooks:      a.Hooks,
		Timeout:    a.QueryTimeout,
		Feedback:   a.Feedback,
	}
}

// Close releases provider and audit resources.
func (a *App) Close() error {
	if a.Auditor != nil {
		if closer, ok := a.Auditor.(interface{ Close() }); ok {
			closer.Close()
		}
	}
	return a.Provider.Close()
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
	dsn, err := r.Resolve(cfg.Database.DSN)
	if err != nil {
		return nil, err
	}
	prov, err := newProvider(cfg.Database.Driver, dsn)
	if err != nil {
		return nil, err
	}
	return AssembleWithProvider(cfg, prov)
}

// AssembleWithProvider wires the application using an injected provider (for
// testing with fakes).
func AssembleWithProvider(cfg *config.Config, prov Provider) (*App, error) {
	configurePool(prov, cfg.RateLimit.IOPool, cfg.RateLimit.ConnMaxIdleTime)
	entities, err := configToEntities(cfg.Entities)
	if err != nil {
		return nil, err
	}
	if err := checkDrift(context.Background(), prov, entities); err != nil {
		return nil, err
	}
	reg, err := entity.NewRegistry(entities)
	if err != nil {
		return nil, err
	}
	auth := rbac.NewRoleAuthorizer(reg)
	var feedback cost.FeedbackStore = cost.NoopFeedbackStore{}
	if cfg.Cost.Enabled {
		feedback = cost.NewMemoryStore()
	}
	var gate cost.Gate
	if cfg.Cost.Enabled {
		gate = cost.NewGateFromCapabilities(
			prov.Dialect().Capabilities(),
			prov.Explainer(),
			toThreshold(cfg.Cost),
			feedback,
		)
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
		msk = mask.NewRuleMasker(nil)
	}
	return &App{
		Provider:     prov,
		Dialect:      prov.Dialect(),
		Registry:     reg,
		Authorizer:   auth,
		Masker:       msk,
		Feedback:     feedback,
		Gate:         gate,
		Engine:       eng,
		Tools:        tools,
		ToolFlags:    cfg.Tools,
		DefaultRole:  cfg.Server.Role,
		QueryTimeout: cfg.Cost.QueryTimeout,
		Auditor:      aud,
		Cache:        cc,
	}, nil
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
		Enabled:           c.Enabled,
		SoftScore:         c.SoftScore,
		HardScore:         c.HardScore,
		MaxRows:           c.MaxRows,
		MaxBytes:          c.MaxBytes,
		RejectFullScan:    c.RejectFullScan,
		WhitelistPKPoint:  c.WhitelistPKPoint,
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
	return entity.Entity{
		Name: ec.Name, Source: source, Schema: ec.Schema, Description: ec.Description,
		Kind: parseKind(ec.Kind), Attributes: attrs, Keys: keys, Role: role,
		MCP:         entity.MCPFlags{DMLTools: ec.MCP.DMLTools, CustomTool: ec.MCP.CustomTool},
		RowPolicies: rowPolicies,
	}, nil
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
