package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"runtime/debug"
	"slices"
	"sync"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/audit"
	"github.com/nethinwei/sql-mcp-server/core/bus"
	"github.com/nethinwei/sql-mcp-server/core/cache"
	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/dialect"
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
	return assembleOn(cfg, connections, nil, nil)
}

// assembly is an App being assembled: what assembleOn is given, then what
// each of its steps built, in order.
type assembly struct {
	cfg         *config.Config
	connections Connections
	// shared are the services of the process the App runs on, nil for
	// services of its own; gs are its shared connections, which its work
	// reaches the connections through (nil reaches them as they are).
	shared *Shared
	gs     guards
	// providers are the read connections, by datasource.
	providers   map[string]Provider
	checked     checkedAssembly
	feedback    cost.FeedbackStore
	sources     map[string]tool.DataSource
	escapes     map[string]tool.EscapeRoute
	txBeginners map[string]store.TxBeginner
	prepared    map[string]*store.PreparedDB
	engine      *engine.Engine
	auditor     audit.Auditor
	cache       cache.Cache[[]map[string]any]
}

// assembleOn wires an App on connections, on shared services when shared is
// not nil and on services of its own otherwise, reaching the connections
// through gs.
func assembleOn(cfg *config.Config, connections Connections, shared *Shared, gs guards) (*App, error) {
	a := &assembly{cfg: cfg, connections: connections, shared: shared, gs: gs}
	if err := a.check(); err != nil {
		return nil, err
	}
	if err := a.acquire(); err != nil {
		a.release()
		return nil, err
	}
	return a.app(), nil
}

// check derives what assembly needs from the configuration and the live
// schema, before any resource is acquired.
func (a *assembly) check() (err error) {
	if a.providers, err = readConnections(a.cfg, a.connections); err != nil {
		return err
	}
	if err := checkNativeLimits(a.cfg, a.connections, a.shared); err != nil {
		return err
	}
	b := sharedBus(a.shared)
	unreachable, err := checkDefaultSchemas(b, a.cfg, a.connections)
	if err != nil {
		return err
	}
	a.checked, err = checkAssembly(b, a.cfg, a.providers, unreachable)
	return err
}

// acquire builds the parts that hold resources: the data sources (prepared
// statement caches), the engine and the auditor. release lets go of what it
// built so far.
func (a *assembly) acquire() (err error) {
	cfg := a.cfg
	if a.shared == nil { // shared connections are sized as the App is published (see Shared.apply)
		for _, p := range (&App{Connections: a.connections}).allProviders() {
			configurePool(p, cfg.RateLimit.IOPool, cfg.RateLimit.ConnMaxIdleTime, cfg.RateLimit.ConnMaxLifetime)
		}
	}
	a.feedback = newFeedbackStore(cfg)
	a.sources, a.txBeginners, a.prepared, err = buildDataSources(cfg, a.connections, a.providers, a.feedback, a.gs)
	if err != nil {
		return err
	}
	a.escapes = buildEscapes(cfg, a.connections, a.prepared, a.gs)
	if a.engine, err = newAssembleEngine(cfg, a.shared); err != nil {
		return err
	}
	if a.auditor, err = newAssembleAuditor(cfg, sharedBus(a.shared)); err != nil {
		return err
	}
	a.cache = newAssembleCache(cfg, a.shared)
	return nil
}

func (a *assembly) release() {
	if a.engine != nil {
		a.engine.Close()
	}
	closePrepared(a.prepared)
}

// app is the App of what was assembled.
func (a *assembly) app() *App {
	cfg, checked := a.cfg, a.checked
	defaultName := defaultDatasource(a.providers)
	entities := checked.registry.Entities()
	app := &App{
		Provider:      a.providers[defaultName],
		Providers:     a.providers,
		Connections:   a.connections,
		Prepared:      a.prepared,
		Sources:       a.sources,
		Escapes:       a.escapes,
		defaultSource: defaultName,
		Registry:      checked.registry,
		Unavailable:   unavailableOf(entities, checked.unreachable),
		Unreachable:   errorTexts(checked.unreachable),
		Authorizer:    rbac.NewGrantAuthorizer(checked.registry, checked.policy),
		Masker:        checked.masker,
		Feedback:      a.feedback,
		Engine:        a.engine,
		Tools:         checked.tools,
		Auditor:       a.auditor,
		Cache:         a.cache,
		Budget:        newBudgetManager(cfg),
		Transactions:  tool.NewTransactionManager(cfg.Transactions.TTL, cfg.Transactions.MaxOpen),
		Writes:        tool.NewWriteTracker(),
		ResultMemory:  tool.NewResultMemory(cfg.RateLimit.MaxResultMemory),
		TxBeginners:   a.txBeginners,
		WriteTargets:  tool.BuildWriteTargets(checked.registry),
		config:        cfg,
	}
	applyAssembledAppConfig(app, cfg)
	app.attachProcess(cfg, a.shared)
	app.assessInBackground(cfg, a.connections, entities)
	return app
}

// unavailableOf maps the entities whose table or columns are missing to why;
// those of unreachable datasources are not known to be missing.
func unavailableOf(entities []entity.Entity, unreachable map[string]error) map[string]string {
	out := map[string]string{}
	for _, e := range entities {
		if e.Unavailable != "" && unreachable[e.DataSource] == nil {
			out[e.Name] = e.Unavailable
		}
	}
	return out
}

func errorTexts(errs map[string]error) map[string]string {
	out := make(map[string]string, len(errs))
	for k, err := range errs {
		out[k] = err.Error()
	}
	return out
}

// attachProcess gives the App the process's call state when it runs on
// shared services: that state is the process's, not the configuration's
// (see Shared). An App on its own keeps its own.
func (a *App) attachProcess(cfg *config.Config, shared *Shared) {
	a.callers = callerLimits(cfg)
	if shared == nil {
		a.Transactions.SetConnectionLimit(cfg.Transactions.MaxOpenPerConnection)
		a.Transactions.SetIdleTimeout(cfg.Transactions.IdleTimeout)
		a.Drift = &tool.SchemaDrift{}
		a.keyed = ratelimit.NewKeyed()
		a.keyed.Update(keyLimits(a.callers))
		return
	}
	a.shared, a.generation = shared, shared.generation.Add(1)
	a.Transactions, a.Budget, a.Writes, a.ResultMemory = shared.transactions, shared.budget, shared.writes,
		shared.results
	a.keyed, a.Drift = shared.callers, shared.drift
}

// sharedBus is the bus of shared services; nil without them.
func sharedBus(shared *Shared) *bus.Bus {
	if shared == nil {
		return nil
	}
	return shared.bus
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
// It returns the datasources it could not reach, by the error.
func checkDefaultSchemas(b *bus.Bus, cfg *config.Config, connections Connections) (map[string]error, error) {
	ctx, cancel := context.WithTimeout(context.Background(), introspectTimeout)
	defer cancel()
	unreachable := map[string]error{}
	err := b.Lane(bus.Background).RunWaiting(ctx, func(ctx context.Context) error {
		return compareDefaultSchemas(ctx, cfg, connections, unreachable)
	})
	return unreachable, err
}

func compareDefaultSchemas(
	ctx context.Context, cfg *config.Config, connections Connections, unreachable map[string]error,
) error {
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
			_, current, err := lister.Schemas(ctx)
			if err != nil {
				unreachable[datasource] = fmt.Errorf("connection %q: %w", connection, err)
				break
			}
			defaults[connection] = current
		}
		if unreachable[datasource] != nil {
			continue
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
	// unreachable are the datasources whose schema could not be read, by the
	// error; their entities are unavailable.
	unreachable map[string]error
}

func checkAssembly(
	b *bus.Bus, cfg *config.Config, providers map[string]Provider, unreachable map[string]error,
) (checkedAssembly, error) {
	if err := prepareAssembleProviders(cfg, providers); err != nil {
		return checkedAssembly{}, err
	}
	off, err := offlineAssembly(cfg)
	if err != nil {
		return checkedAssembly{}, err
	}
	entities, err := validateAssembleEntities(b, off.entities, providers, unreachable)
	if err != nil {
		return checkedAssembly{}, err
	}
	reg, err := entity.NewRegistry(entities)
	if err != nil {
		return checkedAssembly{}, err
	}
	return checkedAssembly{
		registry: reg, policy: off.policy, masker: off.masker, tools: off.tools, unreachable: unreachable,
	}, nil
}

// offlineChecked is what assembling a configuration builds before it reads
// any database.
type offlineChecked struct {
	entities []entity.Entity
	policy   rbac.Policy
	masker   mask.Masker
	tools    *tool.Registry
}

// offlineAssembly runs the steps of assembling cfg that need no database.
func offlineAssembly(cfg *config.Config) (offlineChecked, error) {
	entities, err := configToEntities(cfg.Entities)
	if err != nil {
		return offlineChecked{}, err
	}
	policy, err := accessPolicy(cfg)
	if err != nil {
		return offlineChecked{}, fmt.Errorf("assemble: %w", err)
	}
	msk, err := newAssembleMasker(cfg, entities)
	if err != nil {
		return offlineChecked{}, err
	}
	tools, err := tool.NewRegistry(tool.DefaultTools())
	if err != nil {
		return offlineChecked{}, err
	}
	return offlineChecked{entities: entities, policy: policy, masker: msk, tools: tools}, nil
}

// CheckOffline reports whether cfg passes the steps of assembling it that
// need no database (entities, grants, the masker), so a configuration the
// runtime would refuse to load is refused before it is stored or published
// even where validation misses a rule of those steps. What the databases
// hold (tables, columns, default schemas) is checked only when assembling.
func CheckOffline(cfg *config.Config) error {
	_, err := offlineAssembly(cfg)
	return err
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
func validateAssembleEntities(
	b *bus.Bus, entities []entity.Entity, providers map[string]Provider, unreachable map[string]error,
) ([]entity.Entity, error) {
	if err := validateEntityDatasources(entities, providers); err != nil {
		return nil, err
	}
	entities = reconcileAll(b, providers, entities, unreachable)
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

// introspectTimeout bounds the schema reads of one assembly, so a database
// that stops answering fails a startup or reload instead of hanging it.
const introspectTimeout = time.Minute

// reconcileAll reconciles each datasource's entities with its schema, the
// datasources in parallel on b's Background lane, waiting for it however busy
// it is (a startup or reload must not fail as busy). A datasource whose
// schema cannot be read (unreachable, timed out) does not fail the
// configuration: it is recorded in unreachable and its entities are
// unavailable, as are those of the datasources already in it.
func reconcileAll(
	b *bus.Bus, providers map[string]Provider, entities []entity.Entity, unreachable map[string]error,
) []entity.Entity {
	ctx, cancel := context.WithTimeout(context.Background(), introspectTimeout)
	defer cancel()
	out := slices.Clone(entities)
	var mu sync.Mutex
	var wg sync.WaitGroup
	byDatasource := map[string][]int{}
	for i, e := range entities {
		byDatasource[e.DataSource] = append(byDatasource[e.DataSource], i)
	}
	known := maps.Clone(unreachable) // the goroutines below add to unreachable
	for name, prov := range providers {
		at := byDatasource[name]
		scoped := make([]entity.Entity, len(at))
		for n, i := range at {
			scoped[n] = entities[i]
		}
		down := known[name] != nil
		b.Fork(&wg, "reconcile "+name, func(context.Context) {
			var reconciled []entity.Entity
			var err error
			if !down {
				err = b.Lane(bus.Background).RunWaiting(ctx, func(ctx context.Context) (err error) {
					reconciled, err = reconcileSafely(ctx, name, prov, scoped)
					return err
				})
			}
			if down || err != nil {
				reconciled = unreachableEntities(name, scoped)
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				unreachable[name] = err
			}
			for j, e := range reconciled {
				out[at[j]] = e
			}
		})
	}
	wg.Wait()
	for _, name := range slices.Sorted(maps.Keys(unreachable)) {
		slog.Warn("datasource unreachable: its entities are unavailable until a reload reads its schema",
			"datasource", name, "err", unreachable[name])
	}
	return out
}

// unreachableEntities marks the entities of a datasource whose schema could
// not be read unavailable. Agents are told no more than that: the error may
// name hosts.
func unreachableEntities(datasource string, entities []entity.Entity) []entity.Entity {
	out := slices.Clone(entities)
	for i := range out {
		out[i].Unavailable = "its datasource is unreachable"
	}
	return identifyRelations(out, introspect.Physical{Server: "datasource:" + datasource}, nil)
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
	gs guards,
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
		// Each connection's work runs under its own health (see Shared).
		on := func(connection string) store.DB {
			wrap, _ := gs.guard(name, connection, byName[connection])
			return wrap(cached(connection))
		}
		source, err := dataSourceForProvider(
			cfg, name, readers[name], gs.health(name, route.Read), cached(route.Read), feedback, len(connections) == 1,
		)
		if err != nil {
			return nil, nil, prepared, err
		}
		source.DB, source.Write, source.Execute = on(route.Read), on(route.Write), on(route.Execute)
		_, source.ReadTx = gs.guard(name, route.Read, readers[name])
		source.ReadAfterWrite = database.ReadAfterWrite
		sources[name] = source
		_, txBeginners[name] = gs.guard(name, route.Write, byName[route.Write])
	}
	return sources, txBeginners, prepared, nil
}

// buildEscapes routes the SQL escape hatch of each datasource declaring one
// to the connection its sqlEscape names (reached as its other work is: under
// the connection's health and through its prepared statement cache), with
// EXPLAIN on that same connection so the estimate sees what that account
// sees, and the datasource's cost thresholds. No datasource admits the
// hatch while tools.executeSql is off: the switch is one of its conditions.
func buildEscapes(
	cfg *config.Config, connections Connections, prepared map[string]*store.PreparedDB, gs guards,
) map[string]tool.EscapeRoute {
	escapes := map[string]tool.EscapeRoute{}
	if !cfg.Tools.ExecuteSQL {
		return escapes
	}
	for name, byName := range connections {
		database := cfg.Databases[name]
		connection := database.SQLEscape.Connection
		p, ok := byName[connection]
		if connection == "" || !ok {
			continue
		}
		key := name + "/" + connection
		db, found := prepared[key]
		if !found { // a connection no action routes to: closed with the others
			db = store.WithPreparedCache(p, 0)
			prepared[key] = db
		}
		wrap, _ := gs.guard(name, connection, p)
		explainer, _ := planners(p, gs.health(name, connection))
		threshold := cfg.Cost.Threshold()
		threshold.Datasource, threshold.DialectName = name, p.Dialect().Name()
		if !cfg.Cost.EnabledOrDefault() {
			explainer = nil
		}
		escapes[name] = tool.EscapeRoute{
			Connection: connection, DB: wrap(db), Dialect: p.Dialect(), Explainer: explainer, Threshold: threshold,
		}
	}
	return escapes
}

func dataSourceForProvider(
	cfg *config.Config,
	name string,
	prov Provider,
	h *health,
	db *store.PreparedDB,
	feedback cost.FeedbackStore,
	legacyExactSQL bool,
) (tool.DataSource, error) {
	explainer, sampler := planners(prov, h)
	caps := prov.Profile()
	if cfg.Cost.EnabledOrDefault() && caps.Has(dialect.Explain) && explainer == nil {
		return tool.DataSource{}, fmt.Errorf(
			"datasource %q (%s) has no EXPLAIN implementation",
			name,
			prov.Dialect().Name(),
		)
	}
	if cfg.Cost.AQE.ExplainAnalyze && (!caps.Has(dialect.ExplainAnalyze) || sampler == nil) {
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
	threshold := cfg.Cost.Threshold()
	threshold.Datasource = name
	threshold.DialectName = prov.Dialect().Name()
	threshold.LegacyExactSQL = legacyExactSQL
	if !cfg.Cost.EnabledOrDefault() {
		explainer = nil // no Estimate layer; mandatory safety layers remain
	}
	gate := cost.NewGateFromCapabilities(caps, explainer, threshold, feedback)
	return tool.DataSource{DB: db, Dialect: prov.Dialect(), Gate: gate, Analyze: analyze}, nil
}

// planners are prov's EXPLAIN and EXPLAIN ANALYZE, under the health of the
// connection when it has one: they reach the database as its queries do.
func planners(prov Provider, h *health) (cost.Explainer, cost.AnalyzeSampler) {
	explainer := prov.Explainer()
	sampler, _ := prov.(cost.AnalyzeSampler)
	if h == nil {
		return explainer, sampler
	}
	if explainer != nil {
		explainer = guardedExplainer{Explainer: explainer, health: h}
	}
	if sampler != nil {
		sampler = guardedSampler{AnalyzeSampler: sampler, health: h}
	}
	return explainer, sampler
}

func newAssembleEngine(cfg *config.Config, shared *Shared) (*engine.Engine, error) {
	if shared != nil {
		return shared.engine(cfg)
	}
	limiter, rps, breaker := rateLimiters(cfg.RateLimit)
	return engine.New(
		engine.WithIOPool(cfg.RateLimit.IOPool),
		engine.WithMaxInflight(cfg.RateLimit.MaxInflight),
		engine.WithLimiter(limiter),
		engine.WithRPSLimiter(rps),
		engine.WithBreaker(breaker),
		engine.WithFailureClassifier(recordProviderFailure),
	)
}

func newAssembleAuditor(cfg *config.Config, b *bus.Bus) (audit.Auditor, error) {
	if !cfg.Audit.Enabled || cfg.Audit.Path == "" {
		return audit.NoopAuditor{}, nil
	}
	sink, err := audit.OpenFileSink(cfg.Audit.Path)
	if err != nil {
		return nil, fmt.Errorf("assemble audit sink: %w", err)
	}
	return audit.NewAsyncAuditorOn(b, sink.Record, sink.Close, cfg.Audit.QueueSize).Durably(sink), nil
}

func newAssembleCache(cfg *config.Config, shared *Shared) cache.Cache[[]map[string]any] {
	if shared != nil {
		return shared.readCache(cfg)
	}
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

// defaultDatasource is the datasource a request names none of: "default",
// or the only one.
func defaultDatasource(providers map[string]Provider) string {
	defaultName := "default"
	if _, ok := providers[defaultName]; !ok && len(providers) == 1 {
		for name := range providers {
			defaultName = name
		}
	}
	return defaultName
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
		SQLEscapeEstimateRequired:  cfg.Cost.SQLEscape.EstimateRequired(),
	}
}

// reconcileSafely reconciles on its own goroutine, so a panic fails assembly
// (and a reload keeps the old configuration) instead of stopping the process.
func reconcileSafely(
	ctx context.Context, name string, prov Provider, entities []entity.Entity,
) (out []entity.Entity, err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("reconciliation panicked", "datasource", name, "panic", r, "stack", string(debug.Stack()))
			err = fmt.Errorf("internal error: %v", r)
		}
	}()
	return reconcileEntities(ctx, name, prov, entities)
}
