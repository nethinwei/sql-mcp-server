// Command runner executes the Agent Eval regression track: a frozen task set
// (eval/regression/tasks.yaml) against a deterministic fixture, driven by any
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
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/postgres"

	pgprov "github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

func main() {
	track := flag.String("track", "regression",
		"eval track: regression (frozen v3 pilot), workload (fixtures/v4), or diagnostic (v5)")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	var err error
	switch *track {
	case "regression":
		err = run(ctx)
	case "workload":
		err = runWorkload(ctx)
	case "diagnostic":
		err = runDiagnostic(ctx)
	default:
		err = fmt.Errorf("unknown track %q (want regression, workload, or diagnostic)", *track)
	}
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// newModelClient reads the shared model endpoint configuration.
func newModelClient() (*modelClient, error) {
	apiKey := os.Getenv("EVAL_API_KEY")
	model := os.Getenv("EVAL_MODEL")
	if apiKey == "" || model == "" {
		return nil, fmt.Errorf("EVAL_API_KEY and EVAL_MODEL are required " +
			"(EVAL_BASE_URL defaults to https://api.openai.com/v1); " +
			"the eval needs a live OpenAI-compatible endpoint")
	}
	return &modelClient{
		baseURL: envOr("EVAL_BASE_URL", "https://api.openai.com/v1"),
		apiKey:  apiKey,
		model:   model,
	}, nil
}

func run(ctx context.Context) error {
	client, err := newModelClient()
	if err != nil {
		return err
	}

	tasks, err := loadTasks("eval/regression/tasks.yaml")
	if err != nil {
		return fmt.Errorf("tasks: %w", err)
	}

	dsn, cleanup, err := startDatabase(ctx)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer cleanup()

	session, closeSession, err := startServer(ctx, dsn, "eval/regression/config.yaml")
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
	if _, err := provider.ExecContext(ctx, fixtureSQL+decoySQL()); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return dsn, cleanup, nil
}

// decoyTables are the plausible-but-irrelevant catalog entries for the v3
// big-schema tasks. Every name here must have a matching entity in
// eval/regression/config.yaml (the bootstrap drift check fails fast on mismatch).
var decoyTables = []string{
	"warehouses", "inventory", "categories", "reviews", "sessions",
	"web_events", "campaigns", "leads", "accounts", "contacts",
	"subscriptions", "payments", "refunds", "suppliers", "stores",
	"regions", "couriers", "coupons", "wishlists", "returns",
}

// decoySQL generates the trivial decoy tables (3 rows each, id + name). The
// trailing ANALYZE is required: these tables are created after fixtureSQL's
// ANALYZE, and without statistics the planner estimates ~1270 rows for a
// 3-row table, which makes the cost gate hard-reject every read on them.
func decoySQL() string {
	var b strings.Builder
	for _, name := range decoyTables {
		fmt.Fprintf(&b,
			"CREATE TABLE eval_%s (id integer PRIMARY KEY, name text NOT NULL);\n", name)
		fmt.Fprintf(&b,
			"INSERT INTO eval_%s (id, name) SELECT n, '%s ' || n FROM generate_series(1, 3) AS n;\n",
			name, name)
	}
	b.WriteString("ANALYZE;\n")
	return b.String()
}

const fixtureSQL = `
CREATE TABLE eval_customer (
	id integer PRIMARY KEY,
	name text NOT NULL,
	email text NOT NULL,
	city text NOT NULL,
	tenant_id integer NOT NULL
);
-- Emails are opaque (md5 of the row index) so a masked email cannot be
-- reverse-mapped to a customer id by string pattern; the mask tasks grade
-- the governance contract, not fixture guessability.
INSERT INTO eval_customer (id, name, email, city, tenant_id)
SELECT n,
	(ARRAY['Alice','Bruno','Chloe','Daniel','Elena','Felix','Grace','Hugo','Iris','Jonas',
		'Klara','Liam','Mona','Nils','Olga','Piotr','Quinn','Rosa','Stefan','Tara'])[n],
	'u' || substr(md5(n::text), 1, 12) || '@example.com',
	(ARRAY['Berlin','Paris','Oslo'])[(n - 1) % 3 + 1],
	CASE WHEN n <= 12 THEN 7 ELSE 8 END
FROM generate_series(1, 20) AS n;

-- created_at spans 2025-01-01 .. 2025-07-19 one order per day (time tasks:
-- February has orders 32..59 = 28, June 1 onward is orders 152..200 = 49).
-- fee_cents = n * 101 makes the dollar value (e.g. 42.42) never a substring
-- of the cent value (4242), so unit tasks can grade actual conversion.
CREATE TABLE eval_order (
	id integer PRIMARY KEY,
	customer_id integer NOT NULL,
	status text NOT NULL,
	amount_cents integer NOT NULL,
	fee_cents integer NOT NULL,
	created_at date NOT NULL
);
INSERT INTO eval_order (id, customer_id, status, amount_cents, fee_cents, created_at)
SELECT n,
	(n - 1) % 20 + 1,
	(ARRAY['pending','paid','shipped','cancelled'])[(n - 1) % 4 + 1],
	n * 100,
	n * 101,
	DATE '2025-01-01' + (n - 1)
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

-- Daily-grain fact table for the grain task: 90 days (2025-01-01 ..
-- 2025-03-31), views = day index, so February totals (32+59)*28/2 = 1274.
CREATE TABLE eval_page_view_daily (
	id integer PRIMARY KEY,
	day date NOT NULL,
	views integer NOT NULL
);
INSERT INTO eval_page_view_daily (id, day, views)
SELECT n, DATE '2025-01-01' + (n - 1), n
FROM generate_series(1, 90) AS n;

-- Integer-coded enum for the enum task: priority cycles 1,2,3 over 30 rows
-- (10 each); the 1=low/2=medium/3=high mapping lives only in the field
-- description in eval/regression/config.yaml.
CREATE TABLE eval_ticket (
	id integer PRIMARY KEY,
	priority integer NOT NULL
);
INSERT INTO eval_ticket (id, priority)
SELECT n, (n - 1) % 3 + 1
FROM generate_series(1, 30) AS n;

ANALYZE;
`

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) int {
	if value := os.Getenv(name); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}
