package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/core/tool"
	"github.com/nethinwei/sql-mcp-server/x/configyaml"
	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
)

// ErrRestartRequired marks a configuration change that cannot be applied by
// hot reload or publish without restarting the server.
var ErrRestartRequired = errors.New("config change requires restart")

// CheckHotReload reports whether next can replace old without a restart.
// File reload, store reload and store publish share this rule: transport,
// address, auth/TLS/trusted proxy, the tool set, custom procedure tools,
// switching users on or off, and transaction ttl/maxOpen need a restart.
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
	if procedureToolSignature(next.Entities) != procedureToolSignature(old.Entities) {
		changed = append(changed, "custom procedure tools")
	}
	if (len(next.Users) > 0) != (len(old.Users) > 0) {
		changed = append(changed, "users first configured or all removed")
	}
	return changed
}

// LoadRevision verifies a revision's content hash and decodes its payload
// under the store-mode secret rule.
func LoadRevision(rev revision.Revision) (*config.Config, error) {
	if err := rev.Verify(); err != nil {
		return nil, err
	}
	cfg, err := LoadBytes(rev.Payload)
	if err != nil {
		return nil, err
	}
	if err := ValidateStorePayload(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// CheckPublishable verifies that target loads under the store rules and,
// when current is published (ID != 0), that switching to target needs no
// restart unless restartRequired is set.
func CheckPublishable(current, target revision.Revision, restartRequired bool) error {
	next, err := LoadRevision(target)
	if err != nil {
		return fmt.Errorf("revision %d: %w", target.ID, err)
	}
	if current.ID == 0 {
		return nil
	}
	cur, err := LoadRevision(current)
	if err != nil {
		return fmt.Errorf("published revision %d: %w", current.ID, err)
	}
	if err := CheckHotReload(cur, next); err != nil && !restartRequired {
		return fmt.Errorf("%w; allow it with restart-required (CLI --restart-required, API restartRequired) "+
			"to apply it on the next restart", err)
	}
	return nil
}

// procedureToolSignature identifies the custom procedure tools as registered
// with MCP clients (name, description and input schema), which are fixed at
// startup: renaming a parameter changes the schema and needs a restart.
func procedureToolSignature(entities []config.EntityConfig) string {
	sigs := make([]string, 0)
	for _, ec := range entities {
		if ec.Kind != "procedure" || !ec.MCP.CustomTool || !ec.MCP.TrustedProcedure {
			continue
		}
		info := tool.ProcedureTool{Entity: entity.Entity{Name: ec.Name, Params: ec.Params}}.Info()
		sigs = append(sigs, info.Name+"\x00"+info.Description+"\x00"+string(info.InputSchema))
	}
	sort.Strings(sigs)
	return strings.Join(sigs, "\x01")
}

// LoadBytes decodes, defaults and validates YAML configuration bytes and
// checks that every driver is registered, like Load does for a file.
func LoadBytes(data []byte) (*config.Config, error) {
	cfg, err := configyaml.Decode(data)
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
	databases := cfg.Databases
	if len(databases) == 0 {
		databases = map[string]config.DatabaseConfig{"default": cfg.Database}
	}
	for name, db := range databases {
		for _, secret := range dsnSecrets(db.Driver, db.DSN) {
			// An empty password is no credential (trust or peer authentication).
			if secret.value != "" && !wholePlaceholderRe.MatchString(secret.value) {
				return fmt.Errorf("%w: database %q DSN has a plaintext password; use ${ENV} or ${file:...}",
					ErrPlaintextSecret, name)
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

// WatchStore polls store for a newly published revision and reloads it with
// build. current is the revision the runtime already serves. Failures keep
// the current snapshot (fail-static) and are recorded in Stale; a later
// successful reload clears it.
func (r *Runtime) WatchStore(
	ctx context.Context,
	store revision.Store,
	current int64,
	interval time.Duration,
	build func(revision.Revision) (*App, error),
	onError func(error),
) error {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	report := func(err error) {
		if onError != nil {
			onError(err)
		}
	}
	r.applied.Store(current)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		rev, err := store.Published(ctx)
		if err != nil {
			report(fmt.Errorf("store poll: %w", err))
			continue
		}
		if rev.ID == current {
			continue
		}
		err = rev.Verify()
		if err == nil {
			err = r.reloadWith(func() (*App, error) { return build(rev) })
		}
		if err != nil {
			r.markStale(rev.ID, err, report)
			continue
		}
		r.stale.Store(nil)
		current = rev.ID
		r.applied.Store(current)
	}
}
