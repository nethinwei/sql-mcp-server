package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/nethinwei/sql-mcp-server/version"
	pgprov "github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

const configPlaceholder = "/absolute/path/to/config.yaml"

type manifest struct {
	MCPServers map[string]manifestServer `json:"mcpServers"`
}

type manifestServer struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	dsn, cleanup, err := startDatabase(ctx)
	if err != nil {
		fail("database: %v", err)
	}
	defer cleanup()

	server, err := loadManifest("mcp_config.json")
	if err != nil {
		fail("manifest: %v", err)
	}
	session, err := connect(ctx, server, dsn)
	if err != nil {
		fail("connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	if err := verify(ctx, session); err != nil {
		fail("verify: %v", err)
	}
	fmt.Println("ModelScope smoke passed: manifest, stdio, allow, masking, and deny paths verified")
}

func startDatabase(ctx context.Context) (string, func(), error) {
	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("modelscope"),
		postgres.WithUsername("modelscope"),
		postgres.WithPassword("modelscope"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = container.Terminate(context.Background()) }
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	provider, err := pgprov.New(dsn)
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	defer func() { _ = provider.Close() }()
	_, err = provider.ExecContext(ctx, `
		CREATE TABLE app_user (id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY, email text, tenant_id integer);
		INSERT INTO app_user (email, tenant_id) VALUES ('alice@example.com', 7), ('bob@example.com', 8)`)
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	return dsn, cleanup, nil
}

func loadManifest(path string) (manifestServer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return manifestServer{}, err
	}
	var decoded manifest
	if err := json.Unmarshal(data, &decoded); err != nil {
		return manifestServer{}, err
	}
	server, ok := decoded.MCPServers["sql-mcp-server"]
	if !ok {
		return manifestServer{}, fmt.Errorf("mcpServers.sql-mcp-server is required")
	}
	if server.Command != "sql-mcp-server" {
		return manifestServer{}, fmt.Errorf("command = %q, want sql-mcp-server", server.Command)
	}
	if server.Env["DATABASE_DSN"] == "" {
		return manifestServer{}, fmt.Errorf("DATABASE_DSN example is required")
	}
	if !contains(server.Args, configPlaceholder) {
		return manifestServer{}, fmt.Errorf("config placeholder is required")
	}
	return server, nil
}

func connect(ctx context.Context, server manifestServer, dsn string) (*mcp.ClientSession, error) {
	args := append([]string(nil), server.Args...)
	for index, value := range args {
		if value == configPlaceholder {
			args[index] = "examples/modelscope/config.yaml"
		}
	}
	binary := envOr("MODELSCOPE_BINARY", "./sql-mcp-server")
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = append(os.Environ(), "DATABASE_DSN="+dsn)
	client := mcp.NewClient(&mcp.Implementation{Name: "modelscope-smoke", Version: version.String()}, nil)
	return client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
}

func verify(ctx context.Context, session *mcp.ClientSession) error {
	if err := verifyTools(ctx, session); err != nil {
		return err
	}
	if err := verifyAllowedRead(ctx, session); err != nil {
		return err
	}
	return verifyDeniedReads(ctx, session)
}

func verifyTools(ctx context.Context, session *mcp.ClientSession) error {
	result, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		return err
	}
	names := make(map[string]bool, len(result.Tools))
	for _, tool := range result.Tools {
		names[tool.Name] = true
	}
	if !names["read_records"] || names["delete_record"] {
		return fmt.Errorf(
			"unexpected tool visibility: read=%v delete=%v",
			names["read_records"],
			names["delete_record"],
		)
	}
	return nil
}

func verifyAllowedRead(ctx context.Context, session *mcp.ClientSession) error {
	result, err := callRead(ctx, session, map[string]any{
		"entity": "users",
		"filter": []map[string]any{{"field": "id", "op": "eq", "value": 1}},
	})
	if err != nil || result.IsError {
		return fmt.Errorf("authorized read failed: error=%v result=%s", err, resultText(result))
	}
	text := resultText(result)
	if strings.Contains(text, "alice@example.com") || !strings.Contains(text, "a***") {
		return fmt.Errorf("email masking not applied: %s", text)
	}
	return nil
}

func verifyDeniedReads(ctx context.Context, session *mcp.ClientSession) error {
	isolated, err := callRead(ctx, session, map[string]any{
		"entity": "users",
		"filter": []map[string]any{{"field": "id", "op": "eq", "value": 2}},
	})
	if err != nil || isolated.IsError || strings.Contains(resultText(isolated), "bob") {
		return fmt.Errorf("tenant isolation failed: error=%v result=%s", err, resultText(isolated))
	}
	for name, arguments := range map[string]map[string]any{
		"full scan":    {"entity": "users"},
		"hidden field": {"entity": "users", "fields": []string{"tenant_id"}},
	} {
		result, callErr := callRead(ctx, session, arguments)
		if callErr != nil || !result.IsError {
			return fmt.Errorf("%s was not rejected: error=%v result=%s", name, callErr, resultText(result))
		}
	}
	return nil
}

func callRead(ctx context.Context, session *mcp.ClientSession, args map[string]any) (*mcp.CallToolResult, error) {
	return session.CallTool(ctx, &mcp.CallToolParams{Name: "read_records", Arguments: args})
}

func resultText(result *mcp.CallToolResult) string {
	if result == nil {
		return "<nil>"
	}
	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Sprintf("<marshal error: %v>", err)
	}
	return string(data)
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
