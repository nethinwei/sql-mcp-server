package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/version"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/mcpserver"
	otelhooks "github.com/nethinwei/sql-mcp-server/x/otel"
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
	case "validate":
		return runValidate(args, stdout)
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
	transport := fs.String("transport", "stdio", "transport: stdio | http")
	addr := fs.String("addr", ":8080", "http listen address")
	role := fs.String("role", "", "runtime role (overrides config)")
	watch := fs.Bool("watch", false, "reload config when its contents change")
	watchInterval := fs.Duration("watch-interval", time.Second, "config polling interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := bootstrap.Load(*configPath)
	if err != nil {
		return err
	}
	if *role != "" {
		cfg.Server.Role = *role
	}
	resolveServeEndpoint(fs, cfg, transport, addr)
	build := serveReloadBuilder(*role, cfg.Server, cfg.Tools, toolDiscoverySignature(cfg.Entities))
	app, err := bootstrap.Assemble(cfg)
	if err != nil {
		return err
	}
	app.Hooks = otelhooks.NewHooks()
	runtime := bootstrap.NewRuntimeWithBuilder(app, build)
	defer func() { _ = runtime.Close() }()
	if *watch {
		go serveConfigWatcher(ctx, runtime, *configPath, *watchInterval)
	}
	return serveTransport(ctx, runtime, cfg, *transport, *addr)
}

func serveReloadBuilder(
	role string,
	server config.ServerConfig,
	tools config.ToolFlags,
	discovery string,
) func(string) (*bootstrap.App, error) {
	return func(path string) (*bootstrap.App, error) {
		next, err := bootstrap.Load(path)
		if err != nil {
			return nil, err
		}
		if err := validateHotReloadConfig(server, tools, next, discovery); err != nil {
			return nil, err
		}
		if role != "" {
			next.Server.Role = role
		}
		app, err := bootstrap.Assemble(next)
		if err != nil {
			return nil, err
		}
		app.Hooks = otelhooks.NewHooks()
		return app, nil
	}
}

func serveConfigWatcher(ctx context.Context, runtime *bootstrap.Runtime, path string, interval time.Duration) {
	err := runtime.Watch(ctx, path, interval, func(err error) {
		log.Printf("config reload failed; keeping previous snapshot: %v", err)
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("config watcher stopped: %v", err)
	}
}

func serveTransport(ctx context.Context, runtime *bootstrap.Runtime, cfg *config.Config, transport, addr string) error {
	srv := mcpserver.NewRuntimeServer(runtime)
	switch transport {
	case "stdio":
		err := mcpserver.ServeStdio(ctx, srv)
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	case "http":
		return mcpserver.ServeHTTP(ctx, srv, mcpserver.HTTPConfig{
			Addr: addr, Token: cfg.Server.Auth.Token,
			TrustProxyHeaders: cfg.Server.Auth.TrustProxyHeaders,
			TrustedProxyCIDRs: cfg.Server.Auth.TrustedProxyCIDRs,
			TLSCert:           cfg.Server.Auth.TLS.Cert, TLSKey: cfg.Server.Auth.TLS.Key,
			ClientCA: cfg.Server.Auth.TLS.ClientCA, OnSessionClosed: runtime.RollbackSession,
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

func validateHotReloadConfig(
	server config.ServerConfig,
	tools config.ToolFlags,
	next *config.Config,
	discovery ...string,
) error {
	if next.Server.Transport != server.Transport ||
		next.Server.Addr != server.Addr ||
		!reflect.DeepEqual(next.Server.Auth, server.Auth) ||
		!reflect.DeepEqual(next.Tools, tools) {
		return errors.New(
			"config reload requires restart for transport, address, auth/TLS/trusted proxy, or tool-set changes",
		)
	}
	if len(discovery) > 0 && toolDiscoverySignature(next.Entities) != discovery[0] {
		return errors.New("config reload requires restart when custom procedure tools change")
	}
	return nil
}

func toolDiscoverySignature(entities []config.EntityConfig) string {
	names := make([]string, 0)
	for _, entity := range entities {
		if entity.Kind == "procedure" && entity.MCP.CustomTool && entity.MCP.TrustedProcedure {
			names = append(names, entity.Name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, "\x00")
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
