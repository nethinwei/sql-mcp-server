//go:build e2e

package mcpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"go.uber.org/goleak"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/mcpserver"
	pgprov "github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

// The suite shares one PostgreSQL container: starting a container is most of
// an e2e test's time, while the code under test answers in milliseconds.
// Each test gets a database of its own, so tests stay isolated.
var e2ePG struct {
	once      sync.Once
	container *postgres.PostgresContainer
	admin     *pgprov.Provider
	base      *url.URL
	err       error
	databases atomic.Int64
}

// TestMain stops the shared container and verifies no goroutine leaks across
// the e2e suite.
func TestMain(m *testing.M) {
	code := m.Run()
	if e2ePG.admin != nil {
		_ = e2ePG.admin.Close()
	}
	if e2ePG.container != nil {
		_ = e2ePG.container.Terminate(context.Background())
	}
	if err := goleak.Find(); code == 0 && err != nil {
		fmt.Fprintln(os.Stderr, "goleak:", err)
		code = 1
	}
	os.Exit(code)
}

func sharedE2EPostgres() error {
	e2ePG.once.Do(func() {
		ctx := context.Background()
		e2ePG.container, e2ePG.err = postgres.Run(ctx, "postgres:16-alpine",
			postgres.WithDatabase("test"),
			postgres.WithUsername("test"),
			postgres.WithPassword("test"),
			postgres.BasicWaitStrategies(),
		)
		if e2ePG.err != nil {
			return
		}
		var dsn string
		if dsn, e2ePG.err = e2ePG.container.ConnectionString(ctx, "sslmode=disable"); e2ePG.err != nil {
			return
		}
		if e2ePG.base, e2ePG.err = url.Parse(dsn); e2ePG.err != nil {
			return
		}
		e2ePG.admin, e2ePG.err = pgprov.New(dsn)
	})
	return e2ePG.err
}

// startE2EPostgres connects to a new database of the shared container with
// the users fixture.
func startE2EPostgres(t *testing.T) (*pgprov.Provider, func()) {
	t.Helper()
	if err := sharedE2EPostgres(); err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	ctx := context.Background()
	name := fmt.Sprintf("e2e_%d", e2ePG.databases.Add(1))
	if _, err := e2ePG.admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	dsn := *e2ePG.base
	dsn.Path = "/" + name
	prov, err := pgprov.New(dsn.String())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = prov.ExecContext(ctx, "CREATE TABLE users (id serial PRIMARY KEY, email text, tenant_id integer)")
	_, _ = prov.ExecContext(ctx, "INSERT INTO users (email, tenant_id) VALUES ('alice@x.com', 7), ('bob@x.com', 8)")
	// A separate table: one relation backs at most one entity.
	_, _ = prov.ExecContext(ctx, "CREATE TABLE admin_users (id serial PRIMARY KEY, email text)")
	return prov, func() { _ = prov.Close() }
}

func e2eTestConfig() *config.Config {
	cfg := &config.Config{
		Server:   config.ServerConfig{Role: "operator"},
		Database: config.DatabaseConfig{Driver: "postgres", DSN: "ignored"},
		Entities: []config.EntityConfig{
			{
				Name: "users", Source: "users", Kind: "table", PrimaryKey: []string{"id"},
				Fields: []config.FieldConfig{{Name: "id"}, {Name: "email", Mask: "email"}, {Name: "tenant_id"}},
				Roles:  config.RoleConfig{Read: []string{"operator"}, Update: []string{"operator"}},
				FieldACL: map[string]config.FieldACLConfig{
					"operator": {Read: []string{"id", "email"}, Write: []string{"email"}},
				},
				RowPolicies: config.RowPolicies{
					"operator": config.FilterConfig{"op": "eq", "field": "tenant_id", "value": 7},
				},
			},
			{
				Name: "admin_users", Source: "admin_users", Kind: "table", PrimaryKey: []string{"id"},
				Fields: []config.FieldConfig{{Name: "id"}, {Name: "email"}},
				Roles:  config.RoleConfig{Read: []string{"admin"}},
			},
		},
		Tools: config.DefaultToolFlags(),
		Cost: config.CostConfig{
			Enabled: new(true), SoftScore: 60, HardScore: 40, MaxRows: 10000,
			RejectFullScan: true, WhitelistPKPoint: true,
		},
	}
	cfg.ApplyDefaults()
	return cfg
}

func setupApp(t *testing.T) (*bootstrap.App, func()) {
	t.Helper()
	prov, cleanup := startE2EPostgres(t)
	app, err := bootstrap.AssembleWithProvider(e2eTestConfig(), prov)
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	return app, func() {
		_ = app.Close()
		cleanup()
	}
}

func connectE2ESession(t *testing.T, app *bootstrap.App) (*mcp.ClientSession, context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv := mcpserver.NewServer(app)
	stServer, stClient := mcp.NewInMemoryTransports()
	go func() { _ = srv.Run(ctx, stServer) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e-test"}, nil)
	session, err := client.Connect(ctx, stClient, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	return session, ctx, cancel
}

func TestE2EReleaseCapabilities(t *testing.T) {
	app, cleanup := setupApp(t)
	defer cleanup()
	session, ctx, cancel := connectE2ESession(t, app)
	defer cancel()
	defer session.Close()

	assertE2EToolVisibility(t, ctx, session)
	assertE2EPKReadAndMasking(t, ctx, session)
	assertE2ERLSAndRBAC(t, ctx, session)
	assertE2EUnsafeWriteRejected(t, ctx, session)
	assertE2ETransactionLifecycle(t, ctx, session)
	assertE2EFullScanRejected(t, ctx, session)
	assertE2ESchemaResourceAndPrompts(t, ctx, session)
}

func assertE2EToolVisibility(t *testing.T, ctx context.Context, session *mcp.ClientSession) {
	t.Helper()
	lt, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool, len(lt.Tools))
	for _, tl := range lt.Tools {
		names[tl.Name] = true
	}
	if !names["read_records"] {
		t.Fatal("read_records not registered")
	}
	if names["delete_record"] {
		t.Fatal("delete_record should be off by default")
	}
	for _, want := range []string{"begin_transaction", "rollback_transaction"} {
		if !names[want] {
			t.Fatalf("%s not registered", want)
		}
	}
}

func assertE2EPKReadAndMasking(t *testing.T, ctx context.Context, session *mcp.ClientSession) {
	t.Helper()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "read_records",
		Arguments: map[string]any{
			"entity": "users",
			"filter": []map[string]any{{"field": "id", "op": "eq", "value": 1}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("PK lookup should succeed: %+v", res)
	}
	if !contentContains(res, "a***@x.com") || contentContains(res, "alice@x.com") {
		t.Fatalf("email masking not applied: %+v", res.Content)
	}
}

func assertE2ERLSAndRBAC(t *testing.T, ctx context.Context, session *mcp.ClientSession) {
	t.Helper()
	rls, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "read_records",
		Arguments: map[string]any{
			"entity": "users",
			"filter": []map[string]any{{"field": "id", "op": "eq", "value": 2}},
		},
	})
	if err != nil || rls.IsError {
		t.Fatalf("RLS read failed: result=%+v error=%v", rls, err)
	}
	var rows []map[string]any
	decodeTextContent(t, rls, &rows)
	if len(rows) != 0 {
		t.Fatalf("RLS leaked tenant 8 row: %+v", rows)
	}
	for name, arguments := range map[string]map[string]any{
		"entity RBAC": {"entity": "admin_users"},
		"field ACL":   {"entity": "users", "fields": []string{"tenant_id"}},
	} {
		denied, callErr := session.CallTool(ctx, &mcp.CallToolParams{Name: "read_records", Arguments: arguments})
		if callErr != nil {
			t.Fatalf("%s call: %v", name, callErr)
		}
		if !denied.IsError {
			t.Fatalf("%s should be rejected: %+v", name, denied)
		}
	}
}

func assertE2EUnsafeWriteRejected(t *testing.T, ctx context.Context, session *mcp.ClientSession) {
	t.Helper()
	write, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "update_record",
		Arguments: map[string]any{
			"entity": "users", "filter": []map[string]any{}, "set": map[string]any{"email": "unsafe@x.com"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !write.IsError {
		t.Fatalf("unsafe write should be rejected: %+v", write)
	}
}

func assertE2ETransactionLifecycle(t *testing.T, ctx context.Context, session *mcp.ClientSession) {
	t.Helper()
	begin, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "begin_transaction", Arguments: map[string]any{},
	})
	if err != nil || begin.IsError {
		t.Fatalf("begin transaction failed: result=%+v error=%v", begin, err)
	}
	var transaction []map[string]any
	decodeTextContent(t, begin, &transaction)
	if len(transaction) != 1 {
		t.Fatalf("unexpected transaction result: %+v", transaction)
	}
	token, _ := transaction[0]["transaction"].(string)
	if token == "" {
		t.Fatalf("missing transaction token: %+v", transaction)
	}
	rollback, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "rollback_transaction", Arguments: map[string]any{"transaction": token},
	})
	if err != nil || rollback.IsError {
		t.Fatalf("rollback transaction failed: result=%+v error=%v", rollback, err)
	}
}

func assertE2EFullScanRejected(t *testing.T, ctx context.Context, session *mcp.ClientSession) {
	t.Helper()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "read_records",
		Arguments: map[string]any{"entity": "users"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("full scan should be rejected with IsError")
	}
}

func assertE2ESchemaResourceAndPrompts(t *testing.T, ctx context.Context, session *mcp.ClientSession) {
	t.Helper()
	resources, err := session.ListResources(ctx, &mcp.ListResourcesParams{})
	if err != nil || len(resources.Resources) != 1 {
		t.Fatalf("schema resource list = %+v, error=%v", resources, err)
	}
	schema, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: resources.Resources[0].URI})
	if err != nil || len(schema.Contents) != 1 {
		t.Fatalf("authorized schema = %+v, error=%v", schema, err)
	}
	if !strings.Contains(schema.Contents[0].Text, `"users"`) ||
		strings.Contains(schema.Contents[0].Text, `"admin_users"`) ||
		strings.Contains(schema.Contents[0].Text, `"tenant_id"`) {
		t.Fatalf("authorized schema = %+v", schema)
	}
	prompts, err := session.ListPrompts(ctx, &mcp.ListPromptsParams{})
	if err != nil || len(prompts.Prompts) != 3 {
		t.Fatalf("prompts = %+v, error=%v", prompts, err)
	}
}

func contentContains(res *mcp.CallToolResult, want string) bool {
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok && strings.Contains(tc.Text, want) {
			return true
		}
	}
	return false
}

func decodeTextContent(t *testing.T, res *mcp.CallToolResult, target any) {
	t.Helper()
	for _, content := range res.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			if err := json.Unmarshal([]byte(text.Text), target); err != nil {
				t.Fatalf("decode tool content %q: %v", text.Text, err)
			}
			return
		}
	}
	t.Fatal("tool result has no text content")
}
