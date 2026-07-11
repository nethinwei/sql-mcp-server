package bootstrap

import (
	"context"
	"fmt"

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
// embedders; ownership transfers to the returned App on success.
func AssembleWithProviders(cfg *config.Config, providers map[string]Provider) (*App, error) {
	if err := prepareAssembleProviders(cfg, providers); err != nil {
		return nil, err
	}
	entities, err := configToEntities(cfg.Entities)
	if err != nil {
		return nil, err
	}
	if err := validateAssembleEntities(entities, providers); err != nil {
		return nil, err
	}
	reg, err := entityRegistryFromConfig(entities)
	if err != nil {
		return nil, err
	}
	feedback := newFeedbackStore(cfg)
	sources, txBeginners, prepared, err := buildDataSources(cfg, providers, feedback)
	if err != nil {
		return nil, err
	}
	eng, err := newAssembleEngine(cfg)
	if err != nil {
		return nil, err
	}
	tools, err := tool.NewRegistry(tool.DefaultTools())
	if err != nil {
		eng.Close()
		return nil, err
	}
	aud, err := newAssembleAuditor(cfg, eng, providers)
	if err != nil {
		return nil, err
	}
	cc := newAssembleCache(cfg)
	msk, err := newAssembleMasker(cfg, entities)
	if err != nil {
		return nil, err
	}
	defaultName, defaultSource := defaultDatasource(providers, sources)
	return newAssembledApp(
		cfg, providers, prepared, sources, txBeginners, reg, aud, cc, msk, feedback,
		defaultSource, defaultName, eng, tools,
	), nil
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

func validateAssembleEntities(entities []entity.Entity, providers map[string]Provider) error {
	if err := validateEntityDatasources(entities, providers); err != nil {
		return err
	}
	return checkAllDrift(providers, entities)
}

func validateEntityDatasources(entities []entity.Entity, providers map[string]Provider) error {
	for _, e := range entities {
		if _, ok := providers[e.DataSource]; !ok {
			return fmt.Errorf("entity %q references unavailable datasource %q", e.Name, e.DataSource)
		}
	}
	return nil
}

func checkAllDrift(providers map[string]Provider, entities []entity.Entity) error {
	for name, prov := range providers {
		var scoped []entity.Entity
		for _, e := range entities {
			if e.DataSource == name {
				scoped = append(scoped, e)
			}
		}
		if err := checkDrift(context.Background(), prov, scoped); err != nil {
			return fmt.Errorf("datasource %q: %w", name, err)
		}
	}
	return nil
}

func entityRegistryFromConfig(entities []entity.Entity) (*entity.Registry, error) {
	return entity.NewRegistry(entities)
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
			return nil, nil, nil, err
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
		threshold.DisableEstimate = true
		threshold.SoftScore = 0
		threshold.HardScore = 0
		threshold.RejectFullScan = false
		threshold.RequireKnownScan = false
		threshold.RequireFreshStats = false
		threshold.ExplainFailClosed = false
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
		engine.WithCPUPool(cfg.RateLimit.CPUPool),
		engine.WithMaxInflight(cfg.RateLimit.MaxInflight),
		engine.WithLimiter(limiter),
		engine.WithRPSLimiter(rps),
		engine.WithBreaker(breaker),
		engine.WithFailureClassifier(recordProviderFailure),
	)
}

func newAssembleAuditor(cfg *config.Config, eng *engine.Engine, providers map[string]Provider) (audit.Auditor, error) {
	if !cfg.Audit.Enabled || cfg.Audit.Path == "" {
		return audit.NoopAuditor{}, nil
	}
	sink, err := audit.OpenFileSink(cfg.Audit.Path)
	if err != nil {
		eng.Close()
		closeProviders(providers)
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
	rm := mask.NewRuleMasker(nil)
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
		Authorizer:   rbac.NewRoleAuthorizer(reg),
		Masker:       msk,
		Feedback:     feedback,
		Analyze:      defaultSource.Analyze,
		Gate:         defaultSource.Gate,
		Engine:       eng,
		Tools:        tools,
		Auditor:      aud,
		Cache:        cc,
		Budget:       newBudgetManager(cfg.Budget),
		Transactions: tool.NewTransactionManager(cfg.Transactions.TTL, cfg.Transactions.MaxOpen),
		TxBeginners:  txBeginners,
	}
	applyAssembledAppConfig(app, cfg)
	return app
}

func applyAssembledAppConfig(app *App, cfg *config.Config) {
	app.ToolFlags = cfg.Tools
	app.DefaultRole = cfg.Server.Role
	app.QueryTimeout = cfg.Cost.QueryTimeout
	app.MaxRows = cfg.Cost.MaxRows
	app.MaxProcedureRows = cfg.Cost.MaxProcedureRows
	app.MaxReturnedBytes = cfg.Cost.MaxBytes
	app.MaxINListSize = cfg.Cost.MaxINListSize
	app.MaxFilterConditions = cfg.Cost.MaxFilterConditions
	app.MaxGroupByFields = cfg.Cost.MaxGroupByFields
	app.MaxAggregates = cfg.Cost.MaxAggregates
	app.MaxExpand = cfg.Cost.MaxExpand
	app.CacheMaxEntryRows = cfg.Cache.MaxEntryRows
	app.CacheMaxEntryBytes = cfg.Cache.MaxEntryBytes
	app.TransactionBeginTimeout = cfg.Transactions.BeginTimeout
	app.TransactionCommitTimeout = cfg.Transactions.CommitTimeout
	app.TransactionRollbackTimeout = cfg.Transactions.RollbackTimeout
}
