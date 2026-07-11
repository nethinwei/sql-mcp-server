// Command sql-mcp-server is the entry point. It loads a config file, assembles
// the application, and serves MCP over stdio or streamable HTTP.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runCLI(ctx, os.Args[1:], os.Stdout)
}
