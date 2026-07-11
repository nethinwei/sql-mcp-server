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
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/nethinwei/sql-mcp-server/config"
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
	immutableServer := cfg.Server
	immutableTools := cfg.Tools
	build := func(path string) (*bootstrap.App, error) {
		next, err := bootstrap.Load(path)
		if err != nil {
			return nil, err
		}
		if err := validateHotReloadConfig(immutableServer, immutableTools, next); err != nil {
			return nil, err
		}
		if *role != "" {
			next.Server.Role = *role
		}
		app, err := bootstrap.Assemble(next)
		if err != nil {
			return nil, err
		}
		app.Hooks = otelhooks.NewHooks()
		return app, nil
	}
	app, err := bootstrap.Assemble(cfg)
	if err != nil {
		return err
	}
	app.Hooks = otelhooks.NewHooks()
	runtime := bootstrap.NewRuntimeWithBuilder(app, build)
	defer func() { _ = runtime.Close() }()
	if *watch {
		go func() {
			err := runtime.Watch(ctx, *configPath, *watchInterval, func(err error) {
				log.Printf("config reload failed; keeping previous snapshot: %v", err)
			})
			if err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("config watcher stopped: %v", err)
			}
		}()
	}
	srv := mcpserver.NewRuntimeServer(runtime)
	switch *transport {
	case "stdio":
		err = mcpserver.ServeStdio(ctx, srv)
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	case "http":
		return mcpserver.ServeHTTP(ctx, srv, mcpserver.HTTPConfig{
			Addr: *addr, Token: cfg.Server.Auth.Token,
			TrustProxyHeaders: cfg.Server.Auth.TrustProxyHeaders,
			TrustedProxyCIDRs: cfg.Server.Auth.TrustedProxyCIDRs,
			TLSCert:           cfg.Server.Auth.TLS.Cert, TLSKey: cfg.Server.Auth.TLS.Key,
			ClientCA: cfg.Server.Auth.TLS.ClientCA, OnSessionClosed: runtime.RollbackSession,
		})
	default:
		return errors.New("unknown transport: " + *transport)
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

func validateHotReloadConfig(server config.ServerConfig, tools config.ToolFlags, next *config.Config) error {
	if next.Server.Transport != server.Transport ||
		next.Server.Addr != server.Addr ||
		!reflect.DeepEqual(next.Server.Auth, server.Auth) ||
		!reflect.DeepEqual(next.Tools, tools) {
		return errors.New("config reload requires restart for transport, address, auth/TLS/trusted proxy, or tool-set changes")
	}
	return nil
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
	defer file.Close()
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
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return errors.New("config root must be an object")
	}
	root := doc.Content[0]
	var entities *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "entities" {
			entities = root.Content[i+1]
			break
		}
	}
	if entities == nil {
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "entities"},
			&yaml.Node{Kind: yaml.SequenceNode},
		)
		entities = root.Content[len(root.Content)-1]
	}
	if entities.Kind != yaml.SequenceNode {
		return errors.New("entities must be a list")
	}
	for _, item := range entities.Content {
		for i := 0; i+1 < len(item.Content); i += 2 {
			if item.Content[i].Value == "name" && item.Content[i+1].Value == *name {
				return fmt.Errorf("entity %q already exists", *name)
			}
		}
	}
	entityNode := &yaml.Node{Kind: yaml.MappingNode}
	appendYAMLPair(entityNode, "name", *name)
	appendYAMLPair(entityNode, "source", *source)
	if *datasource != "default" {
		appendYAMLPair(entityNode, "datasource", *datasource)
	}
	entityNode.Content = append(entityNode.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "fields"},
		&yaml.Node{Kind: yaml.SequenceNode},
	)
	entities.Content = append(entities.Content, entityNode)
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	return os.WriteFile(*path, out, 0o600)
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
