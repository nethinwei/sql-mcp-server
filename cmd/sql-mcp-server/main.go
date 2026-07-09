// Command sql-mcp-server is the entry point. It loads a config file, assembles
// the application, and serves MCP over stdio or streamable HTTP.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/mcpserver"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	configPath := flag.String("config", "config.yaml", "config file path")
	transport := flag.String("transport", "stdio", "transport: stdio | http")
	addr := flag.String("addr", ":8080", "http listen address")
	role := flag.String("role", "", "runtime role (overrides config)")
	flag.Parse()

	cfg, err := bootstrap.Load(*configPath)
	if err != nil {
		return err
	}
	if *role != "" {
		cfg.Server.Role = *role
	}
	app, err := bootstrap.Assemble(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = app.Close() }()

	srv := mcpserver.NewServer(app)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch *transport {
	case "stdio":
		if err := mcpserver.ServeStdio(ctx, srv); err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
	case "http":
		if err := mcpserver.ServeHTTP(ctx, srv, *addr); err != nil {
			return err
		}
	default:
		return errors.New("unknown transport: " + *transport)
	}
	return nil
}
