// Command benchoverhead measures the data-plane overhead of the governed
// tool path against a direct database query on the same connection pool and
// fixture. It reports p50/p95/p99 for both paths and the per-percentile
// delta, as JSON on stdout. The fixture, iteration count, and query shape are
// fixed so runs are reproducible; see docs/benchmarks/data-plane-overhead.md
// for the method and reporting rules.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/nethinwei/sql-mcp-server/version"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/mcpserver"
	pgprov "github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

const (
	defaultRows       = 10000
	defaultIterations = 2000
	warmupIterations  = 200
	// idSpace bounds the cycled point-lookup keys so both paths touch the
	// same hot set.
	idSpace = 1000
)

type percentiles struct {
	P50Micros int64 `json:"p50Micros"`
	P95Micros int64 `json:"p95Micros"`
	P99Micros int64 `json:"p99Micros"`
}

type report struct {
	Version    string      `json:"version"`
	GoVersion  string      `json:"goVersion"`
	OSArch     string      `json:"osArch"`
	NumCPU     int         `json:"numCpu"`
	Rows       int         `json:"fixtureRows"`
	Iterations int         `json:"iterations"`
	Direct     percentiles `json:"directQuery"`
	Server     percentiles `json:"toolPath"`
	Overhead   percentiles `json:"overhead"`
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	err := run(ctx)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	rows := envInt("BENCH_ROWS", defaultRows)
	iterations := envInt("BENCH_ITERATIONS", defaultIterations)

	dsn, cleanup, err := startDatabase(ctx, rows)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer cleanup()

	app, err := assembleApp(dsn)
	if err != nil {
		return fmt.Errorf("assemble: %w", err)
	}
	defer func() { _ = app.Close() }()

	direct, err := benchDirect(ctx, app, iterations)
	if err != nil {
		return fmt.Errorf("direct path: %w", err)
	}
	server, err := benchToolPath(ctx, app, iterations)
	if err != nil {
		return fmt.Errorf("tool path: %w", err)
	}

	out := report{
		Version:    version.String(),
		GoVersion:  runtime.Version(),
		OSArch:     runtime.GOOS + "/" + runtime.GOARCH,
		NumCPU:     runtime.NumCPU(),
		Rows:       rows,
		Iterations: iterations,
		Direct:     summarize(direct),
		Server:     summarize(server),
	}
	out.Overhead = percentiles{
		P50Micros: out.Server.P50Micros - out.Direct.P50Micros,
		P95Micros: out.Server.P95Micros - out.Direct.P95Micros,
		P99Micros: out.Server.P99Micros - out.Direct.P99Micros,
	}
	encoded, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}

func startDatabase(ctx context.Context, rows int) (string, func(), error) {
	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("benchoverhead"),
		postgres.WithUsername("benchoverhead"),
		postgres.WithPassword("benchoverhead"),
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
	// generate_series with deterministic derived values: the fixture is a
	// pure function of the row count.
	_, err = provider.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE app_user (id integer PRIMARY KEY, email text NOT NULL, tenant_id integer NOT NULL);
		INSERT INTO app_user (id, email, tenant_id)
		SELECT n, 'user' || n || '@example.com', n %% 100 FROM generate_series(1, %d) AS n;
		ANALYZE app_user`, rows))
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	return dsn, cleanup, nil
}

func assembleApp(dsn string) (*bootstrap.App, error) {
	if err := os.Setenv("DATABASE_DSN", dsn); err != nil {
		return nil, err
	}
	cfg, err := bootstrap.Load("internal/benchoverhead/config.yaml")
	if err != nil {
		return nil, err
	}
	return bootstrap.Assemble(cfg)
}

// benchDirect measures the same point lookup through the provider's pool
// directly, bypassing every gateway layer.
func benchDirect(ctx context.Context, app *bootstrap.App, iterations int) ([]time.Duration, error) {
	query := `SELECT id, email, tenant_id FROM public.app_user WHERE id = $1`
	run := func(i int) error {
		rows, err := app.Provider.QueryContext(ctx, query, i%idSpace+1)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, tenant any
			var email any
			if err := rows.Scan(&id, &email, &tenant); err != nil {
				_ = rows.Close()
				return err
			}
		}
		return rows.Close()
	}
	return measure(iterations, run)
}

// benchToolPath measures the full governed path: in-memory MCP client,
// read_records tool, RBAC, cost gate, engine, masking, serialization.
func benchToolPath(ctx context.Context, app *bootstrap.App, iterations int) ([]time.Duration, error) {
	srv := mcpserver.NewServer(app)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serveCtx, stopServe := context.WithCancel(ctx)
	defer stopServe()
	go func() { _ = srv.Run(serveCtx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "benchoverhead", Version: "dev"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = session.Close() }()

	run := func(i int) error {
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "read_records", Arguments: map[string]any{
			"entity": "users",
			"filter": []map[string]any{{"field": "id", "op": "eq", "value": i%idSpace + 1}},
		}})
		if err != nil {
			return err
		}
		if res.IsError {
			return fmt.Errorf("tool call rejected: %v", res.StructuredContent)
		}
		return nil
	}
	return measure(iterations, run)
}

func measure(iterations int, run func(int) error) ([]time.Duration, error) {
	for i := 0; i < warmupIterations; i++ {
		if err := run(i); err != nil {
			return nil, fmt.Errorf("warmup iteration %d: %w", i, err)
		}
	}
	samples := make([]time.Duration, 0, iterations)
	for i := 0; i < iterations; i++ {
		start := time.Now()
		if err := run(i); err != nil {
			return nil, fmt.Errorf("iteration %d: %w", i, err)
		}
		samples = append(samples, time.Since(start))
	}
	return samples, nil
}

func summarize(samples []time.Duration) percentiles {
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return percentiles{
		P50Micros: percentile(sorted, 50).Microseconds(),
		P95Micros: percentile(sorted, 95).Microseconds(),
		P99Micros: percentile(sorted, 99).Microseconds(),
	}
}

func percentile(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	index := (len(sorted)*p + 99) / 100
	if index > 0 {
		index--
	}
	return sorted[index]
}

func envInt(name string, fallback int) int {
	if value := os.Getenv(name); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}
