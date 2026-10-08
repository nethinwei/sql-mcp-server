package bootstrap

import (
	"context"
	"fmt"
	"slices"

	"github.com/nethinwei/sql-mcp-server/core/audit"
	"github.com/nethinwei/sql-mcp-server/core/cache"
	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/engine"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/mask"
	"github.com/nethinwei/sql-mcp-server/core/ratelimit"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/core/tool"
)

// AssembleWithProviders wires named providers. It is intended for tests and
// embedders; ownership transfers to the returned App on success. On failure
// the providers stay with the caller and every resource acquired here is
// released: configuration is checked before any resource is acquired.
func AssembleWithProviders(cfg *config.Config, providers map[string]Provider) (*App, error) {
	checked, err := checkAssembly(cfg, providers)
	if err != nil {
		return nil, err
	}
	feedback := newFeedbackStore(cfg)
	sources, txBeginners, prepared, err := buildDataSources(cfg, providers, feedback)
	if err != nil {
		closePrepared(prepared)
		return nil, err
	}
	eng, err := newAssembleEngine(cfg)
	if err != nil {
		closePrepared(prepared)
		return nil, err
	}
	aud, err := newAssembleAuditor(cfg)
	if err != nil {
		eng.Close()
		closePrepared(prepared)
		return nil, err
	}
	defaultName, defaultSource := defaultDatasource(providers, sources)
	return newAssembledApp(
		cfg, providers, prepared, sources, txBeginners, checked.registry,
		rbac.NewGrantAuthorizer(checked.registry, checked.policy), aud, newAssembleCache(cfg), checked.masker,
		feedback, defaultSource, defaultName, eng, checked.tools,
	), nil
}

// checkedAssembly holds what assembly derives from configuration and the live
// schema before it acquires any resource.
type checkedAssembly struct {
	registry *entity.Registry
	policy   rbac.Policy
	masker   mask.Masker
	tools    *tool.Registry
}

func checkAssembly(cfg *config.Config, providers map[string]Provider) (checkedAssembly, error) {
	if err := prepareAssembleProviders(cfg, providers); err != nil {
		return checkedAssembly{}, err
	}
	entities, err := configToEntities(cfg.Entities)
	if err != nil {
		return checkedAssembly{}, err
	}
	policy, err := accessPolicy(cfg)
	if err != nil {
		return checkedAssembly{}, fmt.Errorf("assemble: %w", err)
	}
	msk, err := newAssembleMasker(cfg, entities)
	if err != nil {
		return checkedAssembly{}, err
	}
	tools, err := tool.NewRegistry(tool.DefaultTools())
	if err != nil {
		return checkedAssembly{}, err
	}
	if entities, err = validateAssembleEntities(entities, providers); err != nil {
		return checkedAssembly{}, err
	}
	reg, err := entity.NewRegistry(entities)
	if err != nil {
		return checkedAssembly{}, err
	}
	return checkedAssembly{registry: reg, policy: policy, masker: msk, tools: tools}, nil
}

func closePrepared(prepared map[string]*store.PreparedDB) {
	for _, db := range prepared {
		_ = db.Close()
	}
}

func prepareAssembleProviders(cfg *config.Config, providers map[string]Provider) error {
	datasources := make([]string, 0, len(providers))
	for name := range providers {
		datasources = append(datasources, name)
	}
	if err := cost.ValidateTemplateScopes(datasources, cfg.Cost.AllowTemplates, cfg.Cost.RejectTemplates); err != nil {
		return fmt.Errorf("assemble: %w", err)
	}
	for _, prov := range providers {
		configurePool(prov, cfg.RateLimit.IOPool, cfg.RateLimit.ConnMaxIdleTime, cfg.RateLimit.ConnMaxLifetime)
	}
	return nil
}

// validateAssembleEntities checks entities against their datasources and
// returns them with database comments as the default descriptions.
func validateAssembleEntities(entities []entity.Entity, providers map[string]Provider) ([]entity.Entity, error) {
	if err := validateEntityDatasources(entities, providers); err != nil {
		return nil, err
	}
	return reconcileAll(providers, entities)
}

func validateEntityDatasources(entities []entity.Entity, providers map[string]Provider) error {
	for _, e := range entities {
		if _, ok := providers[e.DataSource]; !ok {
			return fmt.Errorf("entity %q references unavailable datasource %q", e.Name, e.DataSource)
		}
	}
	return nil
}

func reconcileAll(providers map[string]Provider, entities []entity.Entity) ([]entity.Entity, error) {
	out := slices.Clone(entities)
	for name, prov := range providers {
		var scoped []entity.Entity
		var at []int
		for i, e := range entities {
			if e.DataSource == name {
				scoped = append(scoped, e)
				at = append(at, i)
			}
		}
		reconciled, err := reconcileEntities(context.Background(), prov, scoped)
		if err != nil {
			return nil, fmt.Errorf("datasource %q: %w", name, err)
		}
		for j, e := range reconciled {
			out[at[j]] = e
		}
	}
	return out, nil
}

func newFeedbackStore(cfg *config.Config) cost.FeedbackStore {
	if !cfg.Cost.EnabledOrDefault() {
		return cost.NoopFeedbackStore{}
	}
	return cost.NewAdaptiveMemoryStoreWithBounds(
		cfg.Cost.AQE.WindowSize,
		cfg.Cost.AQE.MaxFingerprints,
		cfg.Cost.AQE.AnomalyFactor,
		cfg.Cost.AQE.AnomalyMinSamples,
		nil,
	)
}

func buildDataSources(
	cfg *config.Config,
	providers map[string]Provider,
	feedback cost.FeedbackStore,
) (map[string]tool.DataSource, map[string]store.TxBeginner, map[string]*store.PreparedDB, error) {
	sources := make(map[string]tool.DataSource, len(providers))
	txBeginners := make(map[string]store.TxBeginner, len(providers))
	prepared := make(map[string]*store.PreparedDB, len(providers))
	for name, prov := range providers {
		txBeginners[name] = prov
		db := store.WithPreparedCache(prov, cfg.Cache.PreparedMaxSize)
		prepared[name] = db
		source, err := dataSourceForProvider(cfg, name, prov, db, feedback, len(providers) == 1)
		if err != nil {
			return nil, nil, prepared, err
		}
		sources[name] = source
	}
	return sources, txBeginners, prepared, nil
}

func dataSourceForProvider(
	cfg *config.Config,
	name string,
	prov Provider,
	db *store.PreparedDB,
	feedback cost.FeedbackStore,
	legacyExactSQL bool,
) (tool.DataSource, error) {
	explainer := prov.Explainer()
	if cfg.Cost.EnabledOrDefault() && prov.Dialect().Capabilities().ExplainCost && explainer == nil {
		return tool.DataSource{}, fmt.Errorf(
			"datasource %q (%s) has no EXPLAIN implementation",
			name,
			prov.Dialect().Name(),
		)
	}
	sampler, supportsAnalyze := prov.(cost.AnalyzeSampler)
	if cfg.Cost.AQE.ExplainAnalyze && !supportsAnalyze {
		return tool.DataSource{}, fmt.Errorf(
			"datasource %q (%s) does not support EXPLAIN ANALYZE sampling",
			name,
			prov.Dialect().Name(),
		)
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
	threshold := toThreshold(cfg.Cost)
	threshold.Datasource = name
	threshold.DialectName = prov.Dialect().Name()
	threshold.LegacyExactSQL = legacyExactSQL
	if !cfg.Cost.EnabledOrDefault() {
		explainer = nil // no Estimate layer; mandatory safety layers remain
	}
	gate := cost.NewGateFromCapabilities(prov.Dialect().Capabilities(), explainer, threshold, feedback)
	return tool.DataSource{DB: db, Dialect: prov.Dialect(), Gate: gate, Analyze: analyze}, nil
}

func newAssembleEngine(cfg *config.Config) (*engine.Engine, error) {
	var limiter *ratelimit.Adaptive
	var breaker *ratelimit.Breaker
	var rps *ratelimit.TokenBucket
	if cfg.RateLimit.EnabledOrDefault() {
		limiter = ratelimit.NewAdaptive(
			int64(cfg.RateLimit.IOPool),
			int64(cfg.RateLimit.MinConcurrency),
			int64(cfg.RateLimit.MaxInflight),
			cfg.RateLimit.RTTThreshold,
		)
		breaker = ratelimit.NewBreaker(int64(cfg.RateLimit.BreakerThreshold), cfg.RateLimit.BreakerCooldown)
		rps = ratelimit.NewTokenBucket(cfg.RateLimit.RPS)
	}
	return engine.New(
		engine.WithIOPool(cfg.RateLimit.IOPool),
		engine.WithMaxInflight(cfg.RateLimit.MaxInflight),
		engine.WithLimiter(limiter),
		engine.WithRPSLimiter(rps),
		engine.WithBreaker(breaker),
		engine.WithFailureClassifier(recordProviderFailure),
	)
}

func newAssembleAuditor(cfg *config.Config) (audit.Auditor, error) {
	if !cfg.Audit.Enabled || cfg.Audit.Path == "" {
		return audit.NoopAuditor{}, nil
	}
	sink, err := audit.OpenFileSink(cfg.Audit.Path)
	if err != nil {
		return nil, fmt.Errorf("assemble audit sink: %w", err)
	}
	return audit.NewAsyncAuditorWithClose(sink.Record, sink.Close, cfg.Audit.QueueSize), nil
}

func newAssembleCache(cfg *config.Config) cache.Cache[[]map[string]any] {
	if !cfg.Cache.Enabled {
		return cache.NoopCache[[]map[string]any]{}
	}
	return cache.NewTTLCache[[]map[string]any](cfg.Cache.TTL, cfg.Cache.MaxSize)
}

func newAssembleMasker(cfg *config.Config, entities []entity.Entity) (mask.Masker, error) {
	if !cfg.Mask.EnabledOrDefault() {
		return mask.NoopMasker{}, nil
	}
	rm := mask.NewRuleMasker()
	if err := validateMaskRules(rm, entities); err != nil {
		return nil, err
	}
	return rm, nil
}

func defaultDatasource(providers map[string]Provider, sources map[string]tool.DataSource) (string, tool.DataSource) {
	defaultName := "default"
	if _, ok := providers[defaultName]; !ok && len(providers) == 1 {
		for name := range providers {
			defaultName = name
		}
	}
	return defaultName, sources[defaultName]
}

func newAssembledApp(
	cfg *config.Config,
	providers map[string]Provider,
	prepared map[string]*store.PreparedDB,
	sources map[string]tool.DataSource,
	txBeginners map[string]store.TxBeginner,
	reg *entity.Registry,
	authz rbac.Authorizer,
	aud audit.Auditor,
	cc cache.Cache[[]map[string]any],
	msk mask.Masker,
	feedback cost.FeedbackStore,
	defaultSource tool.DataSource,
	defaultName string,
	eng *engine.Engine,
	tools *tool.Registry,
) *App {
	app := &App{
		Provider:     providers[defaultName],
		Providers:    providers,
		Prepared:     prepared,
		Sources:      sources,
		Dialect:      defaultSource.Dialect,
		Registry:     reg,
		Authorizer:   authz,
		Masker:       msk,
		Feedback:     feedback,
		Analyze:      defaultSource.Analyze,
		Gate:         defaultSource.Gate,
		Engine:       eng,
		Tools:        tools,
		Auditor:      aud,
		Cache:        cc,
		Budget:       newBudgetManager(cfg.Budget, userBudgets(cfg)),
		Transactions: tool.NewTransactionManager(cfg.Transactions.TTL, cfg.Transactions.MaxOpen),
		TxBeginners:  txBeginners,
	}
	applyAssembledAppConfig(app, cfg)
	return app
}

func applyAssembledAppConfig(app *App, cfg *config.Config) {
	app.ToolFlags = cfg.Tools
	app.DefaultRole = cfg.Server.Role
	app.DefaultUser = cfg.Server.User
	app.Users, app.UserTokens = userDirectory(cfg)
	app.Limits = tool.Limits{
		Timeout: cfg.Cost.QueryTimeout, MaxRows: cfg.Cost.MaxRows, MaxProcedureRows: cfg.Cost.MaxProcedureRows,
		MaxReturnedBytes: cfg.Cost.MaxBytes, MaxINListSize: cfg.Cost.MaxINListSize,
		MaxFilterConditions: cfg.Cost.MaxFilterConditions, MaxGroupByFields: cfg.Cost.MaxGroupByFields,
		MaxAggregates: cfg.Cost.MaxAggregates, MaxExpand: cfg.Cost.MaxExpand,
		CacheMaxEntryRows: cfg.Cache.MaxEntryRows, CacheMaxEntryBytes: cfg.Cache.MaxEntryBytes,
		TransactionBeginTimeout:    cfg.Transactions.BeginTimeout,
		TransactionCommitTimeout:   cfg.Transactions.CommitTimeout,
		TransactionRollbackTimeout: cfg.Transactions.RollbackTimeout,
	}
}
