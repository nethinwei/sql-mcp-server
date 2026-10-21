package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/nethinwei/sql-mcp-server/core/budget"
	"github.com/nethinwei/sql-mcp-server/core/bus"
	"github.com/nethinwei/sql-mcp-server/core/cache"
	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/engine"
	"github.com/nethinwei/sql-mcp-server/core/ratelimit"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/core/tool"
)

// Shared holds what the Apps of one process share across reloads, so the old
// and the new configuration, serving side by side while the old one drains,
// act as one process: the read cache, which a write through either
// invalidates; the execution bus and the IO quota on it, which bound both
// together; and the database connections, reused while their settings stay
// the same, each with its own health (circuit breaker and adaptive limit:
// one database failing or slowing down is held back on its own).
type Shared struct {
	generation atomic.Uint64
	// auditDropped counts the audit events every App of the process dropped.
	auditDropped atomic.Int64
	cache        *cache.TTLCache[[]map[string]any]
	bus          *bus.Bus
	quota        *engine.Quota
	// The state of the process's calls, which outlives every configuration:
	// open transactions, the sessions' budgets, the writes sessions follow,
	// and the results calls hold. Publishing an App applies its limits to
	// them.
	transactions *tool.TransactionManager
	budget       *budget.MemoryManager
	writes       *tool.WriteTracker
	results      *tool.ResultMemory
	callers      *ratelimit.Keyed // each caller's own rate and calls in flight
	drift        *tool.SchemaDrift

	mu        sync.Mutex
	rateLimit *rateLimits            // applied to quota and to each connection's health
	limits    config.RateLimitConfig // the rate limits applied, for connections opened later
	pools     map[string]*sharedConnection
	warned    string // the hard-limit warnings logged last (see checkNativeLimits)
}

// unwarned reports whether warnings differ from the ones the process logged
// last, and remembers them: a reload that changes nothing about them does
// not log them again. Without a Shared every assembly logs.
func (s *Shared) unwarned(warnings string) bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.warned == warnings {
		return false
	}
	s.warned = warnings
	return true
}

// rateLimits are the rate-limit settings, comparable.
type rateLimits struct {
	config.RateLimitConfig // Enabled, MaxResultMemory and the pool's lifetimes cleared
	enabled                bool
}

// rateLimitsOf are cfg's rate limits, but for the result memory bound and
// the lifetimes of pooled connections: they are applied on their own
// (Shared.apply, configurePool), and changing them must not reset the
// connections' health.
func rateLimitsOf(cfg *config.Config) rateLimits {
	out := rateLimits{RateLimitConfig: cfg.RateLimit, enabled: cfg.RateLimit.EnabledOrDefault()}
	out.Enabled, out.MaxResultMemory, out.ConnMaxIdleTime, out.ConnMaxLifetime = nil, 0, 0, 0
	return out
}

type sharedConnection struct {
	provider Provider
	refs     int
	health   *health
	// beginner begins the connection's transactions under its health, the
	// same for every App (transactions compare connections by identity).
	beginner *guardedBeginner
}

// NewShared returns empty shared services on an execution bus of their own.
func NewShared() *Shared {
	return NewSharedOn(bus.New(1, nil))
}

// NewSharedOn returns empty shared services on b, the process's execution
// bus: the IO quota is b's IO semaphore, and publishing an App sets b's
// limits. The caller closes b after the Runtime.
func NewSharedOn(b *bus.Bus) *Shared {
	s := &Shared{
		cache: cache.NewTTLCache[[]map[string]any](0, 0), bus: b, quota: engine.NewQuotaOn(b.IO()),
		pools:        map[string]*sharedConnection{},
		transactions: tool.NewTransactionManager(0, 0),
		budget:       budget.New(nil, nil),
		callers:      ratelimit.NewKeyed(),
		writes:       tool.NewWriteTracker(),
		results:      tool.NewResultMemory(0),
		drift:        &tool.SchemaDrift{},
	}
	s.transactions.UseBus(b)
	return s
}

// Close rolls back the transactions still open; the Runtime closes it last.
func (s *Shared) Close() {
	s.transactions.Close()
}

// Bus returns the execution bus the services run on.
func (s *Shared) Bus() *bus.Bus { return s.bus }

// laneLimits are the lane limits cfg sets.
func laneLimits(cfg *config.Config) map[bus.LaneName]bus.Limits {
	lane := func(l config.LaneConfig) bus.Limits {
		return bus.Limits{MaxConcurrent: l.MaxConcurrent, MaxQueue: l.MaxQueue}
	}
	return map[bus.LaneName]bus.Limits{
		bus.Serve:      {MaxConcurrent: cfg.RateLimit.MaxInflight},
		bus.Admin:      lane(cfg.Execution.Admin),
		bus.Scan:       lane(cfg.Execution.Scan),
		bus.Background: lane(cfg.Execution.Background),
	}
}

// Assemble opens (or reuses) the connections of cfg and wires an App on the
// shared services, resolving secrets like Assemble.
func (s *Shared) Assemble(cfg *config.Config) (*App, error) {
	if len(cfg.Databases) == 0 {
		return nil, errors.New("assemble: no database configured")
	}
	r := EnvFileResolver{AllowedRoots: cfg.Server.Secrets.AllowedRoots}
	connections, gs, keys, err := s.acquire(cfg, r)
	if err != nil {
		return nil, err
	}
	app, err := assembleOn(cfg, connections, s, gs)
	if err != nil {
		s.release(keys)
		return nil, err
	}
	app.scanResolver = r
	app.releaseConnections = func() { s.release(keys) }
	return app, nil
}

// acquire takes a reference on a connection per configured connection,
// opening the ones no App holds with the same settings.
func (s *Shared) acquire(cfg *config.Config, r SecretResolver) (Connections, guards, []string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	connections := make(Connections, len(cfg.Databases))
	gs := guards{}
	var keys []string
	fail := func(err error) (Connections, guards, []string, error) {
		s.releaseLocked(keys)
		return nil, nil, nil, err
	}
	for name, database := range cfg.Databases {
		connections[name] = map[string]Provider{}
		gs[name] = map[string]*sharedConnection{}
		for connection, c := range database.ConnectionsOrDSN() {
			dsn, err := r.Resolve(c.DSN)
			if err != nil {
				return fail(fmt.Errorf("database %q connection %q: %w", name, connection, err))
			}
			key := connectionKey(cfg, database.Driver, dsn, c.Pooler)
			shared := s.pools[key]
			if shared == nil {
				// A database down does not keep the process from serving the
				// others: its schema cannot be read, and its entities are
				// unavailable until it answers (see reconcileAll).
				p, err := openDSN(context.Background(), cfg, database.Driver, dsn, c.Pooler, true)
				if err != nil {
					return fail(fmt.Errorf("database %q connection %q: %w", name, connection, err))
				}
				shared = &sharedConnection{provider: p, health: &health{}}
				shared.health.set(s.healthParts())
				shared.beginner = &guardedBeginner{TxBeginner: p, health: shared.health}
				s.pools[key] = shared
			}
			shared.refs++
			keys = append(keys, key)
			connections[name][connection] = shared.provider
			gs[name][connection] = shared
		}
	}
	return connections, gs, keys, nil
}

// connectionKey names a connection's settings without keeping its DSN.
func connectionKey(cfg *config.Config, driver, dsn, pooler string) string {
	sum := sha256.Sum256([]byte(driver + "\x00" + dsn + "\x00" + pooler + "\x00" +
		strconv.FormatInt(int64(cfg.Cost.QueryTimeout), 10)))
	return hex.EncodeToString(sum[:])
}

// release drops the references acquire took, closing connections no App
// holds any more.
func (s *Shared) release(keys []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releaseLocked(keys)
}

func (s *Shared) releaseLocked(keys []string) {
	for _, key := range keys {
		shared := s.pools[key]
		if shared.refs--; shared.refs == 0 {
			delete(s.pools, key)
			_ = shared.provider.Close()
		}
	}
}

// apply makes app's configuration the shared settings: its rate limits on
// the quota (keeping the limiter and breaker state when they did not change),
// its cache limits and its connection pool sizes. The Runtime calls it as it
// publishes app, never while assembling: a configuration that fails to
// assemble, or is not published, must not change what serves.
func (s *Shared) apply(app *App) {
	cfg := app.config
	s.mu.Lock()
	if applied := rateLimitsOf(cfg); s.rateLimit == nil || *s.rateLimit != applied {
		_, rps, _ := rateLimiters(cfg.RateLimit)
		s.quota.Update(cfg.RateLimit.IOPool, nil, rps, nil)
		s.rateLimit, s.limits = &applied, cfg.RateLimit
		for _, c := range s.pools {
			c.health.set(s.healthParts())
		}
	}
	s.mu.Unlock()
	s.bus.Update(cfg.RateLimit.IOPool, laneLimits(cfg))
	// Open transactions carry on under the new limits; ttl and idleTimeout
	// apply to those begun from now on.
	s.transactions.UpdateLimits(cfg.Transactions.TTL, cfg.Transactions.MaxOpen)
	s.transactions.SetConnectionLimit(cfg.Transactions.MaxOpenPerConnection)
	s.transactions.SetIdleTimeout(cfg.Transactions.IdleTimeout)
	s.budget.UpdateLimits(budgetLimits(cfg))
	s.callers.Update(keyLimits(app.callers))
	s.results.SetCapacity(cfg.RateLimit.MaxResultMemory)
	if cfg.Cache.Enabled {
		s.cache.UpdateLimits(cfg.Cache.TTL, cfg.Cache.MaxSize, cfg.Cache.MaxTotalBytes)
	}
	for _, p := range app.allProviders() {
		configurePool(p, cfg.RateLimit.IOPool, cfg.RateLimit.ConnMaxIdleTime, cfg.RateLimit.ConnMaxLifetime)
	}
}

// engine returns an engine on the shared quota, admitting calls on the bus's
// Serve lane: the Apps of the process share its in-flight bound. It has no
// breaker nor adaptive limiter (recordProviderFailure then decides nothing):
// each shared connection keeps its own health, across reloads.
func (s *Shared) engine(*config.Config) (*engine.Engine, error) {
	return engine.New(
		engine.WithQuota(s.quota),
		engine.WithLane(s.bus.Lane(bus.Serve)),
		engine.WithFailureClassifier(recordProviderFailure),
	)
}

// readCache returns the shared cache, or, when cfg disables caching, one
// that only passes invalidations on to it.
func (s *Shared) readCache(cfg *config.Config) cache.Cache[[]map[string]any] {
	if !cfg.Cache.Enabled {
		return cache.InvalidateOnly[[]map[string]any]{Cache: s.cache}
	}
	return s.cache
}

// healthParts are a connection's new breaker and adaptive limiter under the
// rate limits applied; nil before any are, or when rate limiting is off.
func (s *Shared) healthParts() (*ratelimit.Breaker, *ratelimit.Adaptive) {
	if s.rateLimit == nil {
		return nil, nil
	}
	limiter, _, breaker := rateLimiters(s.limits)
	return breaker, limiter
}

// guards are the shared connections of an App, by datasource and connection
// name: how its work reaches each (see guard).
type guards map[string]map[string]*sharedConnection

// guard is how work reaches the connection of a datasource: under its
// health, its transactions through its shared beginner; p as it is without
// shared services.
func (g guards) guard(datasource, connection string, p Provider) (func(store.DB) store.DB, store.TxBeginner) {
	if c := g[datasource][connection]; c != nil {
		return func(db store.DB) store.DB { return guardedDB{DB: db, health: c.health} }, c.beginner
	}
	return func(db store.DB) store.DB { return db }, p
}

// health is the health of the connection of a datasource; nil without
// shared services.
func (g guards) health(datasource, connection string) *health {
	if c := g[datasource][connection]; c != nil {
		return c.health
	}
	return nil
}

// rateLimiters builds cfg's adaptive limiter, rate limiter and breaker; all
// nil when rate limiting is off.
func rateLimiters(c config.RateLimitConfig) (*ratelimit.Adaptive, *ratelimit.TokenBucket, *ratelimit.Breaker) {
	if !c.EnabledOrDefault() {
		return nil, nil, nil
	}
	return ratelimit.NewAdaptive(int64(c.IOPool), int64(c.MinConcurrency), int64(c.MaxInflight), c.RTTThreshold),
		ratelimit.NewTokenBucket(c.RPS),
		ratelimit.NewBreaker(int64(c.BreakerThreshold), c.BreakerCooldown)
}
