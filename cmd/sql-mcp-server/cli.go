package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/nethinwei/sql-mcp-server/core/audit"
	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/hook"
	"github.com/nethinwei/sql-mcp-server/version"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/configyaml"
	"github.com/nethinwei/sql-mcp-server/x/mcpserver"
	otelhooks "github.com/nethinwei/sql-mcp-server/x/otel"
	"github.com/nethinwei/sql-mcp-server/x/telemetry"
)

func runCLI(ctx context.Context, args []string, stdout io.Writer) error {
	command, args := parseCommand(args)
	switch command {
	case "serve":
		return runServe(ctx, args)
	case "init":
		return runInit(args)
	case "add":
		if len(args) == 0 || args[0] != "entity" {
			return errors.New("usage: sql-mcp-server add entity [flags]")
		}
		return runAddEntity(args[1:])
	case "user":
		if len(args) == 0 || args[0] != "token" {
			return errors.New("usage: sql-mcp-server user token")
		}
		return runUserToken(args[1:], stdout)
	case "store":
		return runStore(ctx, args, stdout)
	case "migrate":
		return runMigrate(ctx, args, stdout)
	case "admin":
		return runAdmin(ctx, args, stdout)
	case "validate":
		return runValidate(args, stdout)
	case "export":
		return runExport(args, stdout)
	case "explain":
		return runExplain(args, stdout)
	case "version":
		if len(args) != 0 {
			return errors.New("version accepts no arguments")
		}
		_, err := fmt.Fprintln(stdout, version.String())
		return err
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

func parseCommand(args []string) (string, []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return "serve", args
	}
	return args[0], args[1:]
}

func runServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := fs.String("config", "config.yaml", "config file path")
	storeFlag := fs.String("store", "", "configuration store <driver>:<dsn> instead of --config (env SQL_MCP_STORE)")
	secretRoots := fs.String("secret-root", "", "comma-separated allowed roots for ${file:...} in the store DSN")
	transport := fs.String("transport", "stdio", "transport: stdio | http")
	addr := fs.String("addr", ":8080", "http listen address")
	role := fs.String("role", "", "runtime role (overrides config)")
	user := fs.String("user", "", "default user for requests without a user identity (overrides config)")
	watch := fs.Bool("watch", false, "reload config when the file changes or a store revision is published")
	watchInterval := fs.Duration("watch-interval", time.Second, "config polling interval (store default 5s)")
	adminFlags := newServeAdminFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	src, err := openServeSource(ctx, *configPath, *storeFlag, *secretRoots, explicit["config"])
	if err != nil {
		return err
	}
	defer src.close()
	cfg := src.startup
	overrides := serveOverrides{role: *role, user: *user}
	if err := overrides.apply(cfg); err != nil {
		return err
	}
	resolveServeEndpoint(fs, cfg, transport, addr)
	metrics, hooks, otelShutdown, err := setupServeTelemetry(ctx)
	if err != nil {
		return err
	}
	defer otelShutdown()
	auths := &preparedAuths{http: *transport == "http", addr: *addr} // what each reload prepares
	build, runtime, err := newServeRuntime(cfg, overrides, hooks, auths)
	if err != nil {
		return err
	}
	defer func() { _ = runtime.Close() }()
	metrics.SetAuditDropped(auditDroppedReader(runtime))
	if *watch {
		go src.watch(ctx, runtime, build, *watchInterval, explicit["watch-interval"])
	}
	adminHandler, err := adminFlags.handler(src, runtime, cfg, *transport, *watch)
	if err != nil {
		return err
	}
	return serveTransport(ctx, runtime, cfg, *transport, *addr, metrics, adminHandler, auths)
}

// newServeRuntime assembles the startup App and returns the reload builder
// with the runtime; both assemble on the services every configuration of the
// process shares (the cache, IO quota and connections). File reloads go
// through build too.
func newServeRuntime(
	cfg *config.Config,
	overrides serveOverrides,
	hooks *hook.Hooks,
	auths *preparedAuths,
) (func(*config.Config) (*bootstrap.App, error), *bootstrap.Runtime, error) {
	shared := bootstrap.NewShared()
	build := serveBuilder(cfg, overrides, hooks, shared, auths)
	app, err := shared.Assemble(cfg)
	if err != nil {
		return nil, nil, err
	}
	app.Hooks = hooks
	return build, bootstrap.NewRuntimeWithBuilder(app, func(path string) (*bootstrap.App, error) {
		next, err := bootstrap.Load(path)
		if err != nil {
			return nil, err
		}
		return build(next)
	}), nil
}

// setupServeTelemetry wires the serve-only observability stack: JSON logs on
// stderr (stdout stays reserved for stdio transports), the metrics collector,
// OTel span export, and the joined lifecycle hooks shared by every snapshot.
func setupServeTelemetry(ctx context.Context) (*telemetry.Metrics, *hook.Hooks, func(), error) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)
	otelShutdown, err := otelhooks.Setup(ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("otel setup: %w", err)
	}
	shutdown := func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = otelShutdown(shutdownCtx)
	}
	metrics := telemetry.NewMetrics()
	hooks := hook.Join(otelhooks.NewHooks(), metrics.Hooks(), telemetry.LogHooks(logger))
	return metrics, hooks, shutdown, nil
}

func auditDroppedReader(runtime *bootstrap.Runtime) func() int64 {
	return func() int64 {
		current := runtime.Current()
		if current == nil {
			return 0
		}
		if async, ok := current.Auditor.(*audit.AsyncAuditor); ok {
			return async.Dropped()
		}
		return 0
	}
}

// serveOverrides carries CLI flags re-applied to every reloaded config.
type serveOverrides struct {
	role, user string
}

func (o serveOverrides) apply(cfg *config.Config) error {
	if o.role != "" {
		cfg.Server.Role = o.role
	}
	return applyUserOverride(cfg, o.user)
}

// applyUserOverride sets server.user from --user and checks that it names an
// enabled user; config validation already covered the file value.
func applyUserOverride(cfg *config.Config, user string) error {
	user = strings.ToLower(strings.TrimSpace(user))
	if user == "" {
		return nil
	}
	u, ok := cfg.Users[user]
	if !ok {
		return fmt.Errorf("--user references unknown user %q", user)
	}
	if u.Disabled {
		return fmt.Errorf("--user %q is disabled", user)
	}
	cfg.Server.User = user
	return nil
}

// serveBuilder returns the reload builder shared by file and store mode: it
// rejects changes that need a restart, re-applies CLI overrides and assembles
// on the services every configuration shares.
func serveBuilder(
	startup *config.Config,
	overrides serveOverrides,
	hooks *hook.Hooks,
	shared *bootstrap.Shared,
	auths *preparedAuths,
) func(*config.Config) (*bootstrap.App, error) {
	return func(next *config.Config) (*bootstrap.App, error) {
		if err := bootstrap.CheckHotReload(startup, next); err != nil {
			return nil, err
		}
		if err := overrides.apply(next); err != nil {
			return nil, err
		}
		auth, err := auths.prepare(next)
		if err != nil {
			return nil, err
		}
		app, err := shared.Assemble(next)
		if err != nil {
			return nil, err
		}
		app.Hooks = hooks
		auths.keep(app, auth)
		return app, nil
	}
}

func serveConfigWatcher(ctx context.Context, runtime *bootstrap.Runtime, path string, interval time.Duration) {
	err := runtime.Watch(ctx, path, interval, func(err error) {
		slog.Error("config reload failed; keeping previous snapshot", "error", err.Error())
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("config watcher stopped", "error", err.Error())
	}
}

func serveTransport(
	ctx context.Context,
	runtime *bootstrap.Runtime,
	cfg *config.Config,
	transport, addr string,
	metrics http.Handler,
	adminHandler http.Handler,
	auths *preparedAuths,
) error {
	if scans, ok := adminHandler.(interface{ Close() }); ok {
		defer scans.Close() // background schema scans
	}
	srv := mcpserver.NewRuntimeServer(runtime)
	switch transport {
	case "stdio":
		err := mcpserver.ServeStdio(ctx, srv)
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	case "http":
		// The current snapshot's: a reload may have published another one.
		if current := runtime.Current(); current != nil && current.Config() != nil {
			cfg = current.Config()
		}
		auth := httpAuth(cfg, runtime)
		return mcpserver.ServeHTTP(ctx, srv, mcpserver.HTTPConfig{
			Addr: addr, Token: auth.Token, TrustProxyHeaders: auth.TrustProxyHeaders,
			TrustedProxyCIDRs: auth.TrustedProxyCIDRs, TLSCert: auth.TLSCert, TLSKey: auth.TLSKey,
			ClientCA: auth.ClientCA, Users: auth.Users, OnSessionClosed: runtime.RollbackSession,
			SnapshotReady: runtime.SnapshotReady, DatabaseReady: runtime.DatabasesReady,
			Metrics: metrics, RevokedPrincipals: runtime.OnRevokedPrincipals,
			Admin: adminHandler, AuthChanges: auths.follow(runtime),
			SnapshotStale: func() int64 {
				if stale, ok := runtime.Stale(); ok {
					return stale.RevisionID
				}
				return 0
			},
		})
	default:
		return errors.New("unknown transport: " + transport)
	}
}

func resolveServeEndpoint(fs *flag.FlagSet, cfg *config.Config, transport, addr *string) {
	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	if !explicit["transport"] {
		*transport = cfg.Server.Transport
	}
	if !explicit["addr"] && cfg.Server.Addr != "" {
		*addr = cfg.Server.Addr
	}
}

func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	path := fs.String("config", "config.yaml", "config file path")
	driver := fs.String("driver", "postgres", "database driver")
	if err := fs.Parse(args); err != nil {
		return err
	}
	content := fmt.Sprintf("version: \"1\"\ndatabase:\n  driver: %s\n  dsn: ${DATABASE_DSN}\nentities: []\n", *driver)
	file, err := os.OpenFile(*path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	_, err = io.WriteString(file, content)
	return err
}

func runAddEntity(args []string) error {
	fs := flag.NewFlagSet("add entity", flag.ContinueOnError)
	path := fs.String("config", "config.yaml", "config file path")
	name := fs.String("name", "", "logical entity name")
	source := fs.String("source", "", "database object name")
	datasource := fs.String("datasource", "default", "datasource name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return errors.New("entity name is required")
	}
	if *source == "" {
		*source = *name
	}
	data, err := os.ReadFile(*path)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	entities, err := entitiesSequenceNode(&doc)
	if err != nil {
		return err
	}
	if err := ensureEntityNameAvailable(entities, *name); err != nil {
		return err
	}
	entities.Content = append(entities.Content, newEntityYAMLNode(*name, *source, *datasource))
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	return os.WriteFile(*path, out, 0o600)
}

func entitiesSequenceNode(doc *yaml.Node) (*yaml.Node, error) {
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("config root must be an object")
	}
	root := doc.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "entities" {
			if root.Content[i+1].Kind != yaml.SequenceNode {
				return nil, errors.New("entities must be a list")
			}
			return root.Content[i+1], nil
		}
	}
	root.Content = append(root.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "entities"},
		&yaml.Node{Kind: yaml.SequenceNode},
	)
	return root.Content[len(root.Content)-1], nil
}

func ensureEntityNameAvailable(entities *yaml.Node, name string) error {
	for _, item := range entities.Content {
		for i := 0; i+1 < len(item.Content); i += 2 {
			if item.Content[i].Value == "name" && item.Content[i+1].Value == name {
				return fmt.Errorf("entity %q already exists", name)
			}
		}
	}
	return nil
}

func newEntityYAMLNode(name, source, datasource string) *yaml.Node {
	entityNode := &yaml.Node{Kind: yaml.MappingNode}
	appendYAMLPair(entityNode, "name", name)
	appendYAMLPair(entityNode, "source", source)
	if datasource != "default" {
		appendYAMLPair(entityNode, "datasource", datasource)
	}
	entityNode.Content = append(entityNode.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "fields"},
		&yaml.Node{Kind: yaml.SequenceNode},
	)
	return entityNode
}

func appendYAMLPair(node *yaml.Node, key, value string) {
	node.Content = append(node.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Value: value},
	)
}

// runUserToken prints a new random bearer token once, with the tokenHash to
// put under users.<name>.tokenHash. Only the hash belongs in configuration.
func runUserToken(args []string, stdout io.Writer) error {
	if len(args) != 0 {
		return errors.New("user token accepts no arguments")
	}
	token, err := config.NewUserToken()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "token: %s\ntokenHash: %s\n", token, config.TokenHash(token))
	return err
}

func runValidate(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	path := fs.String("config", "config.yaml", "config file path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := bootstrap.ValidateFile(*path, bootstrap.EnvFileResolver{}); err != nil {
		return err
	}
	_, err := fmt.Fprintln(stdout, "valid")
	return err
}

// runExport writes the effective configuration as deterministic YAML: field
// order follows the configuration contract (struct order, sorted map keys),
// defaults are materialized by the same loader used at startup, and secret
// placeholders (${ENV} / ${file:...}) are preserved verbatim, never resolved
// to plaintext. Repeated exports of the same config are byte-identical, and
// the output round-trips through `validate`.
func runExport(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	path := fs.String("config", "config.yaml", "config file path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := bootstrap.Load(*path)
	if err != nil {
		return err
	}
	data, err := exportYAML(cfg)
	if err != nil {
		return err
	}
	_, err = stdout.Write(data)
	return err
}

// exportYAML is the deterministic export encoding, also used as the payload
// of configuration store revisions.
func exportYAML(cfg *config.Config) ([]byte, error) {
	return configyaml.Encode(cfg)
}

func runExplain(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("explain", flag.ContinueOnError)
	path := fs.String("config", "config.yaml", "config file path")
	name := fs.String("entity", "", "entity name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := bootstrap.Load(*path)
	if err != nil {
		return err
	}
	type explanation struct {
		Name       string               `json:"name"`
		Source     string               `json:"source"`
		Datasource string               `json:"datasource"`
		Kind       string               `json:"kind"`
		Fields     []config.FieldConfig `json:"fields"`
		Roles      config.RoleConfig    `json:"roles"`
	}
	out := make([]explanation, 0)
	for _, entity := range cfg.Entities {
		if *name != "" && entity.Name != *name {
			continue
		}
		source := entity.Source
		if source == "" {
			source = entity.Name
		}
		datasource := entity.DataSource
		if datasource == "" {
			datasource = "default"
		}
		out = append(out, explanation{
			Name: entity.Name, Source: source, Datasource: datasource,
			Kind: entity.Kind, Fields: entity.Fields, Roles: entity.Roles,
		})
	}
	if *name != "" && len(out) == 0 {
		return fmt.Errorf("entity %q not found", *name)
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(out)
}
