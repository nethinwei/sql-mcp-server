package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/revision"
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
	var changed []string
	if next.Server.Transport != old.Server.Transport || next.Server.Addr != old.Server.Addr {
		changed = append(changed, "transport/address")
	}
	if !reflect.DeepEqual(next.Server.Auth, old.Server.Auth) {
		changed = append(changed, "auth/TLS/trusted proxy")
	}
	if !reflect.DeepEqual(next.Tools, old.Tools) {
		changed = append(changed, "tool set")
	}
	if procedureToolSignature(next.Entities) != procedureToolSignature(old.Entities) {
		changed = append(changed, "custom procedure tools")
	}
	if (len(next.Users) > 0) != (len(old.Users) > 0) {
		changed = append(changed, "users first configured or all removed")
	}
	if next.Transactions.TTL != old.Transactions.TTL || next.Transactions.MaxOpen != old.Transactions.MaxOpen {
		changed = append(changed, "transaction ttl/maxOpen")
	}
	if len(changed) > 0 {
		return fmt.Errorf("%w: %s", ErrRestartRequired, strings.Join(changed, ", "))
	}
	return nil
}

func procedureToolSignature(entities []config.EntityConfig) string {
	names := make([]string, 0)
	for _, e := range entities {
		if e.Kind == "procedure" && e.MCP.CustomTool && e.MCP.TrustedProcedure {
			names = append(names, e.Name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, "\x00")
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

// ValidateStorePayload enforces the store-mode secret rule: every DSN
// password must come from a ${...} placeholder and server.auth.token must be
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
		for _, password := range dsnPasswords(db.DSN) {
			if !wholePlaceholderRe.MatchString(password) {
				return fmt.Errorf("%w: database %q DSN has a plaintext password; use ${ENV} or ${file:...}",
					ErrPlaintextSecret, name)
			}
		}
	}
	return nil
}

var (
	wholePlaceholderRe = regexp.MustCompile(`^\$\{[^}]+\}$`)
	uriPasswordRe      = regexp.MustCompile(`://[^:/@]+:([^@]+)@`)
	mysqlPasswordRe    = regexp.MustCompile(`^[^:@/]+:([^@]+)@`)
	keyPasswordRe      = regexp.MustCompile(`(?i)(?:^|[?;&\s])(?:password|pwd)=([^&;\s]+)`)
)

// dsnPasswords extracts password values from URI (scheme://user:pass@),
// MySQL (user:pass@tcp(...)) and key=value (password=/pwd=) DSN forms. A DSN
// that is a single placeholder has no inline password.
func dsnPasswords(dsn string) []string {
	if wholePlaceholderRe.MatchString(dsn) {
		return nil
	}
	var out []string
	userinfo := mysqlPasswordRe
	if strings.Contains(dsn, "://") {
		userinfo = uriPasswordRe
	}
	if m := userinfo.FindStringSubmatch(dsn); m != nil {
		out = append(out, m[1])
	}
	for _, m := range keyPasswordRe.FindAllStringSubmatch(dsn, -1) {
		out = append(out, m[1])
	}
	return out
}

// StaleState describes a published store revision the runtime has not
// applied, either because building it failed or because it needs a restart.
type StaleState struct {
	RevisionID int64
	Err        string
}

// Stale returns the unapplied published revision, if any.
func (r *Runtime) Stale() (StaleState, bool) {
	s := r.stale.Load()
	if s == nil {
		return StaleState{}, false
	}
	return *s, true
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
			// Retry every tick (a transient failure may clear) but report a
			// revision's failure only when it first appears or changes.
			if prev, ok := r.Stale(); !ok || prev.RevisionID != rev.ID || prev.Err != err.Error() {
				report(fmt.Errorf("apply revision %d: %w", rev.ID, err))
			}
			r.stale.Store(&StaleState{RevisionID: rev.ID, Err: err.Error()})
			continue
		}
		r.stale.Store(nil)
		current = rev.ID
	}
}
