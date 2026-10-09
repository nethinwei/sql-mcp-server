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

	"github.com/nethinwei/sql-mcp-server/core/cache"
	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/engine"
	"github.com/nethinwei/sql-mcp-server/core/ratelimit"
)

// Shared holds what the Apps of one process share across reloads, so the old
// and the new configuration, serving side by side while the old one drains,
// act as one process: the read cache, which a write through either
// invalidates; the IO quota, which bounds both together; and the database
// connections, reused while their settings stay the same.
type Shared struct {
	generation atomic.Uint64
	cache      *cache.TTLCache[[]map[string]any]
	quota      *engine.Quota

	mu        sync.Mutex
	rateLimit *rateLimits // applied to quota
	pools     map[string]*sharedConnection
}

// rateLimits are the rate-limit settings, comparable.
type rateLimits struct {
	config.RateLimitConfig // Enabled cleared
	enabled                bool
}

func rateLimitsOf(cfg *config.Config) rateLimits {
	out := rateLimits{RateLimitConfig: cfg.RateLimit, enabled: cfg.RateLimit.EnabledOrDefault()}
	out.Enabled = nil
	return out
}

type sharedConnection struct {
	provider Provider
	refs     int
}

// NewShared returns empty shared services.
func NewShared() *Shared {
	return &Shared{
		cache: cache.NewTTLCache[[]map[string]any](0, 0), quota: engine.NewQuota(1),
		pools: map[string]*sharedConnection{},
	}
}

// Assemble opens (or reuses) the connections of cfg and wires an App on the
// shared services, resolving secrets like Assemble.
func (s *Shared) Assemble(cfg *config.Config) (*App, error) {
	if len(cfg.Databases) == 0 {
		return nil, errors.New("assemble: no database configured")
	}
	r := EnvFileResolver{AllowedRoots: cfg.Server.Secrets.AllowedRoots}
	connections, keys, err := s.acquire(cfg, r)
	if err != nil {
		return nil, err
	}
	app, err := assembleWith(cfg, connections, s)
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
func (s *Shared) acquire(cfg *config.Config, r SecretResolver) (Connections, []string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	connections := make(Connections, len(cfg.Databases))
	var keys []string
	fail := func(err error) (Connections, []string, error) {
		s.releaseLocked(keys)
		return nil, nil, err
	}
	for name, database := range cfg.Databases {
		connections[name] = map[string]Provider{}
		for connection, c := range database.ConnectionsOrDSN() {
			dsn, err := r.Resolve(c.DSN)
			if err != nil {
				return fail(fmt.Errorf("database %q connection %q: %w", name, connection, err))
			}
			key := connectionKey(cfg, database.Driver, dsn, c.Pooler)
			shared := s.pools[key]
			if shared == nil {
				p, err := openDSN(context.Background(), cfg, database.Driver, dsn, c.Pooler)
				if err != nil {
					return fail(fmt.Errorf("database %q connection %q: %w", name, connection, err))
				}
				shared = &sharedConnection{provider: p}
				s.pools[key] = shared
			}
			shared.refs++
			keys = append(keys, key)
			connections[name][connection] = shared.provider
		}
	}
	return connections, keys, nil
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
		limiter, rps, breaker := rateLimiters(cfg)
		s.quota.Update(cfg.RateLimit.IOPool, limiter, rps, breaker)
		s.rateLimit = &applied
	}
	s.mu.Unlock()
	if cfg.Cache.Enabled {
		s.cache.UpdateLimits(cfg.Cache.TTL, cfg.Cache.MaxSize)
	}
	for _, p := range app.allProviders() {
		configurePool(p, cfg.RateLimit.IOPool, cfg.RateLimit.ConnMaxIdleTime, cfg.RateLimit.ConnMaxLifetime)
	}
}

// engine returns an engine on the shared quota.
func (s *Shared) engine(cfg *config.Config) (*engine.Engine, error) {
	return engine.New(
		engine.WithQuota(s.quota),
		engine.WithMaxInflight(cfg.RateLimit.MaxInflight),
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

// rateLimiters builds cfg's adaptive limiter, rate limiter and breaker; all
// nil when rate limiting is off.
func rateLimiters(cfg *config.Config) (*ratelimit.Adaptive, *ratelimit.TokenBucket, *ratelimit.Breaker) {
	if !cfg.RateLimit.EnabledOrDefault() {
		return nil, nil, nil
	}
	return ratelimit.NewAdaptive(
			int64(cfg.RateLimit.IOPool),
			int64(cfg.RateLimit.MinConcurrency),
			int64(cfg.RateLimit.MaxInflight),
			cfg.RateLimit.RTTThreshold,
		),
		ratelimit.NewTokenBucket(cfg.RateLimit.RPS),
		ratelimit.NewBreaker(int64(cfg.RateLimit.BreakerThreshold), cfg.RateLimit.BreakerCooldown)
}
