package bootstrap

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/core/tool"
	"github.com/nethinwei/sql-mcp-server/x/configyaml"
	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
)

// ErrRestartRequired marks a configuration change that cannot be applied by
// hot reload or publish without restarting the server.
var ErrRestartRequired = errors.New("config change requires restart")

// CheckHotReload reports whether next can replace old without a restart.
// File reload, store reload and store publish share this rule: the
// transport, the address, and serving TLS or not need a restart; the rest,
// authentication and the listed tools included, follows a reload.
func CheckHotReload(old, next *config.Config) error {
	if changed := RestartChanges(old, next); len(changed) > 0 {
		return fmt.Errorf("%w: %s", ErrRestartRequired, strings.Join(changed, ", "))
	}
	return nil
}

// RestartChanges lists the changes from old to next that need a restart.
func RestartChanges(old, next *config.Config) []string {
	// Fields tagged `schema:"restart"`, by configuration path.
	changed := config.RestartFieldChanges(old, next)
	if servesTLS(next) != servesTLS(old) {
		changed = append(changed, "server.auth.tls (TLS turned on or off)")
	}
	return changed
}

// LoadRevision verifies a revision's content hash and decodes its payload
// under the store-mode secret rule.
func LoadRevision(rev revision.Revision) (*config.Config, error) {
	_, cfg, err := LoadRevisionSpec(rev)
	return cfg, err
}

// LoadRevisionSpec is LoadRevision keeping what the payload says too.
func LoadRevisionSpec(rev revision.Revision) (*config.Spec, *config.Config, error) {
	if err := rev.Verify(); err != nil {
		return nil, nil, err
	}
	spec, err := configyaml.DecodeSpec(rev.Payload)
	if err != nil {
		return nil, nil, fmt.Errorf("load config: %w", err)
	}
	cfg, err := loadSpec(spec)
	if err != nil {
		return nil, nil, err
	}
	if err := ValidateStorePayload(cfg); err != nil {
		return nil, nil, err
	}
	return spec, cfg, nil
}

// CheckPublishable verifies that target loads under the store rules and
// passes the offline assembly checks (CheckOffline) and, when current is
// published (ID != 0), that switching to target needs no restart unless
// restartRequired is set. A current revision that no longer loads can only
// be replaced with restartRequired.
func CheckPublishable(current, target revision.Revision, restartRequired bool) error {
	next, err := LoadRevision(target)
	if err == nil {
		err = CheckOffline(next)
	}
	if err != nil {
		return fmt.Errorf("revision %d: %w", target.ID, err)
	}
	if current.ID == 0 {
		return nil
	}
	cur, err := LoadRevision(current)
	if err != nil {
		// Nothing to compare against; replacing it takes a restart anyway.
		if restartRequired {
			return nil
		}
		return fmt.Errorf("published revision %d no longer loads (%w); publish with restart-required "+
			"(CLI --restart-required, API restartRequired) to replace it", current.ID, err)
	}
	if err := CheckHotReload(cur, next); err != nil && !restartRequired {
		return fmt.Errorf("%w; allow it with restart-required (CLI --restart-required, API restartRequired) "+
			"to apply it on the next restart", err)
	}
	return nil
}

// servesTLS reports whether cfg serves HTTP over TLS: the listener's kind.
func servesTLS(cfg *config.Config) bool {
	return cfg.Server.Auth.TLS.Cert != "" && cfg.Server.Auth.TLS.Key != ""
}

// LoadBytes decodes, defaults and validates YAML configuration bytes and
// checks that every driver is registered, like Load does for a file.
func LoadBytes(data []byte) (*config.Config, error) {
	spec, err := configyaml.DecodeSpec(data)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return loadSpec(spec)
}

// loadSpec resolves spec and checks that every driver is registered.
func loadSpec(spec *config.Spec) (*config.Config, error) {
	cfg, err := config.Resolve(spec)
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

// ErrPlaintextSecret rejects store payloads that would persist credentials.
var ErrPlaintextSecret = errors.New("store payload must not contain plaintext secrets")

// ValidateStorePayload enforces the store-mode secret rule: every non-empty
// DSN password must come from a ${...} placeholder and server.auth.token must be
// empty (configured users keep only token hashes).
func ValidateStorePayload(cfg *config.Config) error {
	if cfg.Server.Auth.Token != "" {
		return fmt.Errorf("%w: server.auth.token is set; use users with tokenHash instead", ErrPlaintextSecret)
	}
	for name, db := range cfg.Databases {
		for connection, c := range db.ConnectionsOrDSN() {
			for _, secret := range dsnSecrets(db.Driver, c.DSN) {
				// An empty password is no credential (trust or peer authentication).
				if secret.value != "" && !wholePlaceholderRe.MatchString(secret.value) {
					return fmt.Errorf("%w: database %q connection %q DSN has a plaintext password; "+
						"use ${ENV} or ${file:...}", ErrPlaintextSecret, name, connection)
				}
			}
		}
	}
	return nil
}

var wholePlaceholderRe = regexp.MustCompile(`^\$\{[^}]+\}$`)

// StaleState describes a published store revision the runtime has not
// applied, either because building it failed or because it needs a restart.
type StaleState struct {
	RevisionID int64
	Err        string
	// RestartRequired reports that the revision only applies after a restart.
	RestartRequired bool
}

// Stale returns the unapplied published revision, if any.
func (r *Runtime) Stale() (StaleState, bool) {
	s := r.stale.Load()
	if s == nil {
		return StaleState{}, false
	}
	return *s, true
}

// markStale records that revision id failed to apply. WatchStore retries
// every tick (a transient failure may clear) but reports a revision's failure
// only when it first appears or changes.
func (r *Runtime) markStale(id int64, err error, report func(error)) {
	if prev, ok := r.Stale(); !ok || prev.RevisionID != id || prev.Err != err.Error() {
		report(fmt.Errorf("apply revision %d: %w", id, err))
	}
	r.stale.Store(&StaleState{RevisionID: id, Err: err.Error(), RestartRequired: errors.Is(err, ErrRestartRequired)})
}

// AppliedRevision returns the store revision the runtime serves as tracked by
// WatchStore, or 0 before WatchStore starts.
func (r *Runtime) AppliedRevision() int64 {
	return r.applied.Load()
}

// ReloadWith publishes the App returned by build, with the same budget,
// transaction and drain handling as Reload.
func (r *Runtime) ReloadWith(build func() (*App, error)) error {
	return r.reloadWith(build)
}

// maxRetryDelay bounds the wait before a revision that failed to apply is
// tried again.
const maxRetryDelay = 5 * time.Minute

// WatchStore polls store for a newly published revision and reloads it with
// build. current is the revision the runtime already serves. A poll reads
// only the published revision's summary; its payload is read when it
// changed. Failures keep the current snapshot (fail-static) and are recorded
// in Stale; a later successful reload clears it. A failed revision is tried
// again after a delay doubling from interval up to maxRetryDelay, as
// assembling reads every datasource's schema; a newly published revision is
// tried at once. While datasources of the served revision are unreachable
// (see App.Unreachable), or once a call found the schema of one changed (see
// App.Drift), it is assembled again on the same schedule, so their entities
// come back once the databases answer, and the entities a schema change
// broke are reported unavailable. The users a revision that
// loads but fails to apply changes are held meanwhile (see holdChanged).
func (r *Runtime) WatchStore(
	ctx context.Context,
	store revision.Store,
	current int64,
	interval time.Duration,
	build func(revision.Revision) (*App, error),
	onError func(error),
) error {
	report := reporter(onError)
	r.applied.Store(current)
	base := cmp.Or(interval, 5*time.Second)
	retry, recheck := retryBackoff{base: base}, retryBackoff{base: base}
	return poll(ctx, interval, 5*time.Second, func() {
		head, err := store.PublishedSummary(ctx)
		if err != nil {
			report(fmt.Errorf("store poll: %w", err))
			return
		}
		now := time.Now()
		again := head.ID == current
		if again && (!r.degraded() || !recheck.due(current, now)) || !again && !retry.due(head.ID, now) {
			return
		}
		drifted := again && r.drift().Take()
		rev, err := r.reloadPublished(ctx, store, build)
		switch {
		case err != nil && again:
			if drifted {
				r.drift().Report()
			}
			recheck.failed(current, now)
			report(fmt.Errorf("reassemble revision %d: %w", current, err))
		case err != nil:
			retry.failed(head.ID, now)
			r.markStale(head.ID, err, report)
			if rev.ID == head.ID {
				r.holdLoaded(LoadRevision(rev))
			}
		default:
			r.stale.Store(nil)
			current = rev.ID
			r.applied.Store(current)
			if r.degraded() || drifted {
				// A schema change found again soon is checked after a
				// growing delay, not at every poll.
				recheck.failed(current, now)
			}
		}
	})
}

// reloadPublished reloads the published revision with build.
func (r *Runtime) reloadPublished(
	ctx context.Context, store revision.Store, build func(revision.Revision) (*App, error),
) (revision.Revision, error) {
	rev, err := store.Published(ctx)
	if err == nil {
		err = rev.Verify()
	}
	if err == nil {
		err = r.reloadWith(func() (*App, error) { return build(rev) })
	}
	return rev, err
}

// degraded reports whether datasources of the served App are unreachable,
// or a call found the schema of one changed.
func (r *Runtime) degraded() bool {
	app := r.Current()
	return app != nil && (len(app.Unreachable) > 0 || app.Drift.Pending())
}

// drift is the served App's schema drift signal.
func (r *Runtime) drift() *tool.SchemaDrift {
	if app := r.Current(); app != nil {
		return app.Drift
	}
	return nil
}

// retryBackoff spaces the attempts at a revision that keeps failing.
type retryBackoff struct {
	base    time.Duration
	failing int64 // the revision that failed last; 0 for none
	delay   time.Duration
	at      time.Time // when failing is tried next
}

// due reports whether revision id is to be tried at now.
func (b *retryBackoff) due(id int64, now time.Time) bool {
	return id != b.failing || !now.Before(b.at)
}

// failed records that revision id failed at now.
func (b *retryBackoff) failed(id int64, now time.Time) {
	if id != b.failing {
		b.failing, b.delay = id, b.base
	} else {
		b.delay = min(b.delay*2, maxRetryDelay)
	}
	b.at = now.Add(b.delay)
}
