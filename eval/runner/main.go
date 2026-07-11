// Command runner executes the Agent Eval pilot: a fixed task set
// (eval/tasks.yaml) against a deterministic fixture, driven by any
// OpenAI-compatible chat-completions endpoint with tool calling. It scores
// tasks mechanically (see eval/README.md) and prints a JSON report to stdout.
//
// Configuration (environment):
//
//	EVAL_API_KEY   required; bearer token for the model endpoint
//	EVAL_MODEL     required; model name
//	EVAL_BASE_URL  optional; defaults to https://api.openai.com/v1
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/postgres"

	pgprov "github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	err := run(ctx)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	apiKey := os.Getenv("EVAL_API_KEY")
	model := os.Getenv("EVAL_MODEL")
	if apiKey == "" || model == "" {
		return fmt.Errorf("EVAL_API_KEY and EVAL_MODEL are required " +
			"(EVAL_BASE_URL defaults to https://api.openai.com/v1); " +
			"the pilot needs a live OpenAI-compatible endpoint")
	}
	client := &modelClient{
		baseURL: envOr("EVAL_BASE_URL", "https://api.openai.com/v1"),
		apiKey:  apiKey,
		model:   model,
	}

	tasks, err := loadTasks("eval/tasks.yaml")
	if err != nil {
		return fmt.Errorf("tasks: %w", err)
	}

	dsn, cleanup, err := startDatabase(ctx)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer cleanup()

	session, closeSession, err := startServer(ctx, dsn)
	if err != nil {
		return fmt.Errorf("server: %w", err)
	}
	defer closeSession()

	report, err := runTasks(ctx, client, session, tasks)
	if err != nil {
		return err
	}
	return printReport(report)
}

// startDatabase seeds the deterministic fixture the task answers are graded
// against. Every value is a pure function of the row index; changing it
// invalidates the task set and requires a new task-set version.
func startDatabase(ctx context.Context) (string, func(), error) {
	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("evalpilot"),
		postgres.WithUsername("evalpilot"),
		postgres.WithPassword("evalpilot"),
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
	if _, err := provider.ExecContext(ctx, fixtureSQL); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return dsn, cleanup, nil
}

const fixtureSQL = `
CREATE TABLE eval_customer (
	id integer PRIMARY KEY,
	name text NOT NULL,
	email text NOT NULL,
	city text NOT NULL,
	tenant_id integer NOT NULL
);
INSERT INTO eval_customer (id, name, email, city, tenant_id)
SELECT n,
	(ARRAY['Alice','Bruno','Chloe','Daniel','Elena','Felix','Grace','Hugo','Iris','Jonas',
		'Klara','Liam','Mona','Nils','Olga','Piotr','Quinn','Rosa','Stefan','Tara'])[n],
	'customer' || n || '@example.com',
	(ARRAY['Berlin','Paris','Oslo'])[(n - 1) % 3 + 1],
	CASE WHEN n <= 12 THEN 7 ELSE 8 END
FROM generate_series(1, 20) AS n;

CREATE TABLE eval_order (
	id integer PRIMARY KEY,
	customer_id integer NOT NULL,
	status text NOT NULL,
	amount_cents integer NOT NULL
);
INSERT INTO eval_order (id, customer_id, status, amount_cents)
SELECT n,
	(n - 1) % 20 + 1,
	(ARRAY['pending','paid','shipped','cancelled'])[(n - 1) % 4 + 1],
	n * 100
FROM generate_series(1, 200) AS n;

CREATE TABLE eval_product (
	id integer PRIMARY KEY,
	name text NOT NULL,
	category text NOT NULL,
	price_cents integer NOT NULL
);
INSERT INTO eval_product (id, name, category, price_cents)
SELECT n,
	'Product ' || n,
	CASE WHEN n % 2 = 1 THEN 'gadget' ELSE 'widget' END,
	n * 500
FROM generate_series(1, 10) AS n;

CREATE TABLE eval_employee (
	id integer PRIMARY KEY,
	name text NOT NULL,
	dept text NOT NULL,
	salary_cents integer NOT NULL
);
INSERT INTO eval_employee (id, name, dept, salary_cents)
SELECT n,
	'Employee ' || n,
	CASE WHEN n % 2 = 1 THEN 'sales' ELSE 'eng' END,
	n * 100000
FROM generate_series(1, 8) AS n;

ANALYZE;
`

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
