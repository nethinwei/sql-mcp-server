package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"

	"github.com/nethinwei/sql-mcp-server/core/audit"
	"github.com/nethinwei/sql-mcp-server/core/cache"
	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/engine"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/core/mask"
	"github.com/nethinwei/sql-mcp-server/core/ratelimit"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/core/tool"
)

// AssembleWithConnections wires opened connections. Ownership transfers to
// the returned App on success. On failure the connections stay with the
// caller and every resource acquired here is released: configuration is
// checked before any resource is acquired. Each datasource's read connection
// serves introspection and EXPLAIN.
func AssembleWithConnections(cfg *config.Config, connections Connections) (*App, error) {
	providers, err := readConnections(cfg, connections)
	if err != nil {
		return nil, err
	}
	if err := checkDefaultSchemas(cfg, connections); err != nil {
		return nil, err
	}
	checked, err := checkAssembly(cfg, providers)
	if err != nil {
		return nil, err
	}
	for _, p := range (&App{Connections: connections}).allProviders() {
		configurePool(p, cfg.RateLimit.IOPool, cfg.RateLimit.ConnMaxIdleTime, cfg.RateLimit.ConnMaxLifetime)
	}
	feedback := newFeedbackStore(cfg)
	sources, txBeginners, prepared, err := buildDataSources(cfg, connections, providers, feedback)
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
	app := newAssembledApp(
		cfg, providers, prepared, sources, txBeginners, checked.registry,
		rbac.NewGrantAuthorizer(checked.registry, checked.policy), aud, newAssembleCache(cfg), checked.masker,
		feedback, defaultSource, defaultName, eng, checked.tools,
	)
	app.Connections = connections
	app.Capabilities = assessCapabilities(cfg, connections, checked.registry.Entities())
	for _, warning := range CapabilityWarnings(cfg, app.Capabilities) {
		slog.Warn("grant exceeds the datasource connection's privileges", "detail", warning)
	}
	return app, nil
}

// readConnections returns each datasource's read connection.
func readConnections(cfg *config.Config, connections Connections) (map[string]Provider, error) {
	readers := make(map[string]Provider, len(connections))
	for datasource, byName := range connections {
		name := cfg.Databases[datasource].Route().Read
		p, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("datasource %q has no connection %q", datasource, name)
		}
		readers[datasource] = p
	}
	return readers, nil
}

// checkDefaultSchemas requires the connections of a datasource to resolve
// unqualified names to the same schema (accounts may have different
// search_paths or default databases); otherwise its entities must name one.
func checkDefaultSchemas(cfg *config.Config, connections Connections) error {
	for datasource, byName := range connections {
		if !unqualifiedEntities(cfg, datasource) {
			continue
		}
		defaults := map[string]string{}
		for connection, p := range byName {
			lister, ok := p.Introspector().(introspect.SchemaLister)
			if !ok {
				continue
			}
			_, current, err := lister.Schemas(context.Background())
			if err != nil {
				return fmt.Errorf("datasource %q connection %q: %w", datasource, connection, err)
			}
			defaults[connection] = current
		}
		if len(slices.Compact(slices.Sorted(maps.Values(defaults)))) > 1 {
			return fmt.Errorf("datasource %q: connections resolve unqualified names to different schemas %v; "+
				"set schema on its entities", datasource, defaults)
		}
	}
	return nil
}

func unqualifiedEntities(cfg *config.Config, datasource string) bool {
	return slices.ContainsFunc(cfg.Entities, func(e config.EntityConfig) bool {
		return e.DatasourceName() == datasource && e.Schema == "" && e.Kind != "procedure"
	})
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
	return nil
}

// validateAssembleEntities checks entities against their datasources and
// returns them with database comments as the default descriptions.
func validateAssembleEntities(entities []entity.Entity, providers map[string]Provider) ([]entity.Entity, error) {
	if err := validateEntityDatasources(entities, providers); err != nil {
		return nil, err
	}
	entities, err := reconcileAll(providers, entities)
	if err != nil {
		return nil, err
	}
	return entities, checkUniqueRelations(entities)
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
		reconciled, err := reconcileEntities(context.Background(), name, prov, scoped)
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

// buildDataSources routes each datasource: the read connection (with its
// gate and EXPLAIN) serves reads, and the routed connections writes,
// procedure calls and read-write transactions. A connection behind a
// transaction-mode pooler gets no prepared-statement cache.
func buildDataSources(
	cfg *config.Config,
	connections Connections,
	readers map[string]Provider,
	feedback cost.FeedbackStore,
) (map[string]tool.DataSource, map[string]store.TxBeginner, map[string]*store.PreparedDB, error) {
	sources := make(map[string]tool.DataSource, len(connections))
	txBeginners := make(map[string]store.TxBeginner, len(connections))
	prepared := map[string]*store.PreparedDB{}
	for name, byName := range connections {
		database := cfg.Databases[name]
		cached := func(connection string) *store.PreparedDB {
			key := name + "/" + connection
			if db, ok := prepared[key]; ok {
				return db
			}
			size := cfg.Cache.PreparedMaxSize
			if database.ConnectionsOrDSN()[connection].Pooler != "" {
				size = 0
			}
			prepared[key] = store.WithPreparedCache(byName[connection], size)
			return prepared[key]
		}
		route := database.Route()
		source, err := dataSourceForProvider(cfg, name, readers[name], cached(route.Read), feedback, len(connections) == 1)
		if err != nil {
			return nil, nil, prepared, err
		}
		source.Write, source.Execute = cached(route.Write), cached(route.Execute)
		source.ReadTx, source.ReadAfterWrite = readers[name], database.ReadAfterWrite
		sources[name] = source
		txBeginners[name] = byName[route.Write]
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
		Writes:       tool.NewWriteTracker(),
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
