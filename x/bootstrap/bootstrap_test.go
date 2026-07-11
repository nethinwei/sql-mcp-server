package bootstrap

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/codegen"
	"github.com/nethinwei/sql-mcp-server/config"
	"github.com/nethinwei/sql-mcp-server/cost"
	"github.com/nethinwei/sql-mcp-server/dialect"
	"github.com/nethinwei/sql-mcp-server/engine"
	"github.com/nethinwei/sql-mcp-server/entity"
	"github.com/nethinwei/sql-mcp-server/introspect"
	"github.com/nethinwei/sql-mcp-server/relalg"
	"github.com/nethinwei/sql-mcp-server/store"
	"github.com/nethinwei/sql-mcp-server/tool"
	"github.com/nethinwei/sql-mcp-server/x/providers/mysql"
	"github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

func TestRecordProviderFailureClassification(t *testing.T) {
	t.Parallel()
	for _, err := range []error{
		context.Canceled, context.DeadlineExceeded, tool.ErrUnauthorized,
		tool.ErrInvalidInput, cost.ErrCostExceeded,
	} {
		if recordProviderFailure(err) {
			t.Errorf("business/context error counted as provider failure: %v", err)
		}
	}
	if !recordProviderFailure(errors.New("connection reset")) {
		t.Fatal("provider error was not counted")
	}
}

func TestAppCloseContextDrainsBeforeTransactionsAndProviders(t *testing.T) {
	eng, _ := engine.New(engine.WithIOPool(1), engine.WithMaxInflight(2))
	started := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_, _ = eng.Submit(context.Background(), "", func(context.Context) (any, error) {
			close(started)
			<-release
			return nil, nil
		})
	}()
	<-started
	tx := &store.FakeTx{}
	beginner := &store.FakeDB{BeginFn: func(context.Context, *store.TxOptions) (store.Tx, error) {
		return tx, nil
	}}
	manager := tool.NewTransactionManager(time.Minute, 1)
	if _, err := manager.Begin(context.Background(), beginner, "s", "reader", nil, "default", nil); err != nil {
		t.Fatal(err)
	}
	provider := &fakeProvider{}
	app := &App{Provider: provider, Engine: eng, Transactions: manager}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := app.CloseContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CloseContext error = %v", err)
	}
	if !tx.RolledBack || provider.closed != 1 {
		t.Fatalf("resources were not force-closed after drain timeout: rollback=%v provider=%d", tx.RolledBack, provider.closed)
	}
	close(release)
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	if !tx.RolledBack || provider.closed != 1 {
		t.Fatalf("resources closed more than once: rollback=%v provider=%d", tx.RolledBack, provider.closed)
	}
}

type fakeProvider struct {
	store.FakeDB
	dialect     dialect.Dialect
	explainPlan *cost.Plan
	closed      int
}

func (p *fakeProvider) Dialect() dialect.Dialect { return p.dialect }
func (p *fakeProvider) Explainer() cost.Explainer {
	if p.explainPlan != nil {
		return cost.FakeExplainer{Plan: *p.explainPlan}
	}
	return cost.FakeExplainer{Plan: cost.Plan{
		ScanType: cost.ScanIndex, EstimatedRows: 1, StatsFresh: true,
	}}
}
func (p *fakeProvider) Introspector() introspect.Introspector { return nil }
func (p *fakeProvider) Close() error                          { p.closed++; return nil }
func (p *fakeProvider) QueryContext(context.Context, string, ...any) (store.Rows, error) {
	return store.NewFakeRows([]string{"id"}), nil
}

type fakeAnalyzeProvider struct{ *fakeProvider }

func (p *fakeAnalyzeProvider) ExplainAnalyze(context.Context, codegen.Compiled) (cost.Plan, error) {
	return cost.Plan{ActualRows: 1}, nil
}

func TestLoad(t *testing.T) {
	t.Parallel()
	yamlContent := []byte(`
database:
  driver: postgres
  dsn: postgres://localhost/test
entities:
  - name: users
    source: t_user
    primaryKey: [id]
    fields:
      - name: id
      - name: email
`)
	tmp, err := os.CreateTemp("", "cfg-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(yamlContent); err != nil {
		t.Fatal(err)
	}
	tmp.Close()

	cfg, err := Load(tmp.Name())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.Driver != "postgres" {
		t.Fatalf("driver = %q", cfg.Database.Driver)
	}
	if len(cfg.Entities) != 1 {
		t.Fatalf("entities = %d", len(cfg.Entities))
	}
	if !cfg.Tools.ReadRecords {
		t.Fatal("ApplyDefaults should enable ReadRecords")
	}
	if cfg.Entities[0].MCP.DMLTools != true {
		t.Fatal("entity MCP.DMLTools should default true")
	}
}

func TestConfigToEntities(t *testing.T) {
	t.Parallel()
	ecs := []config.EntityConfig{
		{
			Name: "users", Source: "t_user", Kind: "table", PrimaryKey: []string{"id"},
			Fields: []config.FieldConfig{{Name: "id"}, {Name: "phone", Exclude: true}},
			Roles:  config.RoleConfig{Read: []string{"reader"}},
			FieldACL: map[string]config.FieldACLConfig{
				"reader": {Read: []string{"id"}},
			},
			MCP: config.MCPFlags{DMLTools: true},
			RowPolicies: config.RowPolicies{
				"reader": config.FilterConfig{"op": "eq", "field": "tenant_id", "value": 7},
			},
		},
	}
	entities, err := configToEntities(ecs)
	if err != nil {
		t.Fatal(err)
	}
	if len(entities) != 1 {
		t.Fatalf("got %d entities", len(entities))
	}
	e := entities[0]
	if e.Source != "t_user" || e.Kind != entity.KindTable {
		t.Fatalf("entity = %+v", e)
	}
	if len(e.Keys) != 1 || e.Keys[0].Columns[0] != "id" || !e.Keys[0].Primary {
		t.Fatalf("keys = %+v", e.Keys)
	}
	if len(e.Attributes) != 2 {
		t.Fatalf("attrs = %+v", e.Attributes)
	}
	if got := e.FieldAccess["reader"].Read; len(got) != 1 || got[0] != "id" {
		t.Fatalf("field ACL = %+v", e.FieldAccess)
	}
	rp := e.RowPolicies["reader"]
	if rp == nil {
		t.Fatal("row policy not set")
	}
	c, ok := rp.(relalg.Condition)
	if !ok || c.Field != "tenant_id" || c.Value != 7 {
		t.Fatalf("row policy = %+v", rp)
	}
}

func TestFilterConfigToPredicate(t *testing.T) {
	t.Parallel()
	p, err := filterConfigToPredicate(config.FilterConfig{"op": "eq", "field": "id", "value": 1})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := p.(relalg.Condition)
	if !ok || c.Op != relalg.OpEq || c.Field != "id" {
		t.Fatalf("got %+v", p)
	}

	p2, err := filterConfigToPredicate(config.FilterConfig{
		"and": []any{
			config.FilterConfig{"op": "gt", "field": "id", "value": 0},
			config.FilterConfig{"op": "is_null", "field": "deleted_at"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	a, ok := p2.(relalg.And)
	if !ok || len(a.Preds) != 2 {
		t.Fatalf("got %+v", p2)
	}
}

func TestResolveSecrets(t *testing.T) {
	t.Parallel()
	os.Setenv("TEST_DSN_VAR", "postgres://x")
	defer os.Unsetenv("TEST_DSN_VAR")
	got, err := resolveSecrets("host=${TEST_DSN_VAR}")
	if err != nil {
		t.Fatal(err)
	}
	if got != "host=postgres://x" {
		t.Fatalf("got %q", got)
	}
	if _, err := resolveSecrets("${MISSING_VAR_ZZZ}"); err == nil {
		t.Fatal("expected error for missing env")
	}
}

func TestNewProviderUnsupported(t *testing.T) {
	t.Parallel()
	if _, err := newProvider("oracle", "", time.Second); err == nil {
		t.Fatal("expected error for unsupported driver")
	}
	// mysql with an invalid DSN fails fast at ping (still an error).
	if _, err := newProvider("mysql", "", time.Second); err == nil {
		t.Fatal("expected error for invalid mysql dsn")
	}
}

func TestRedactDSN(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"postgres://user:secret@host:5432/db", "postgres://user:%2A%2A%2A@host:5432/db"},
		{"postgresql://u:p@h/db", "postgresql://u:%2A%2A%2A@h/db"},
		{"user:secret@tcp(127.0.0.1:3306)/db", "user:***@tcp(127.0.0.1:3306)/db"},
		{"postgres://user@host/db", "postgres://user@host/db"},
		{"postgres://user:secret@host/db?password=other", "postgres://user:%2A%2A%2A@host/db?password=***"},
		{"user=x password=secret host=db", "user=x password=*** host=db"},
	}
	for _, c := range cases {
		if got := RedactDSN(c.in); got != c.want {
			t.Errorf("RedactDSN(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestEnvFileResolverRestrictsRoots(t *testing.T) {
	root := t.TempDir()
	path := root + "/dsn"
	if err := os.WriteFile(path, []byte("postgres://db"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver := EnvFileResolver{AllowedRoots: []string{root}}
	got, err := resolver.Resolve("${file:" + path + "}")
	if err != nil {
		t.Fatal(err)
	}
	if got != "postgres://db" {
		t.Fatalf("resolved = %q", got)
	}
	if _, err := resolver.Resolve("${file:/etc/passwd}"); err == nil {
		t.Fatal("expected path outside allowed roots to be rejected")
	}
}

func TestEnvFileResolver(t *testing.T) {
	t.Parallel()
	os.Setenv("TEST_RES_VAR", "val")
	defer os.Unsetenv("TEST_RES_VAR")
	r := EnvFileResolver{}
	got, err := r.Resolve("x=${TEST_RES_VAR}")
	if err != nil {
		t.Fatal(err)
	}
	if got != "x=val" {
		t.Fatalf("got %q, want x=val", got)
	}
	if _, err := r.Resolve("${MISSING_RES_ZZZ}"); err == nil {
		t.Fatal("expected error for missing env")
	}
}

func TestAssembleWithNamedProvidersRoutesAndClosesAll(t *testing.T) {
	cfg := &config.Config{
		Databases: map[string]config.DatabaseConfig{
			"main":    {Driver: "postgres", DSN: "x"},
			"archive": {Driver: "mysql", DSN: "y"},
		},
		Entities: []config.EntityConfig{
			{Name: "users", DataSource: "main"},
			{Name: "events", DataSource: "archive"},
		},
	}
	cfg.ApplyDefaults()
	main := &fakeProvider{dialect: postgres.Dialect{}}
	archive := &fakeProvider{dialect: mysql.Dialect{}}
	app, err := AssembleWithProviders(cfg, map[string]Provider{"main": main, "archive": archive})
	if err != nil {
		t.Fatal(err)
	}
	if app.Sources["main"].Dialect.Name() != "postgres" ||
		app.Sources["archive"].Dialect.Name() != "mysql" {
		t.Fatalf("sources = %+v", app.Sources)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	if main.closed != 1 || archive.closed != 1 {
		t.Fatalf("close counts = main:%d archive:%d", main.closed, archive.closed)
	}
}

func TestAssembleKeepsMandatorySafetyWhenCostEstimateDisabled(t *testing.T) {
	cfg := &config.Config{
		Database: config.DatabaseConfig{Driver: "postgres", DSN: "x"},
		Cost:     config.CostConfig{Enabled: config.Bool(false)},
		Entities: []config.EntityConfig{{
			Name: "users", PrimaryKey: []string{"id"},
			Fields: []config.FieldConfig{{Name: "id"}, {Name: "status"}},
			Roles:  config.RoleConfig{Update: []string{"writer"}},
		}},
	}
	cfg.ApplyDefaults()
	app, err := AssembleWithProvider(cfg, &fakeProvider{dialect: postgres.Dialect{}})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	chain, ok := app.Gate.(*cost.ChainGate)
	if !ok {
		t.Fatalf("gate type = %T", app.Gate)
	}
	foundWriteGuard := false
	for _, layer := range chain.Layers() {
		if layer.Name() == "write-guard" {
			foundWriteGuard = true
		}
	}
	if !foundWriteGuard {
		t.Fatal("cost.enabled=false removed mandatory write guard")
	}
}

func TestAssembleUsesConservativeMySQLEstimate(t *testing.T) {
	cfg := &config.Config{
		Database: config.DatabaseConfig{Driver: "mysql", DSN: "x"},
		Entities: []config.EntityConfig{{
			Name: "users", Fields: []config.FieldConfig{{Name: "id"}},
			Roles: config.RoleConfig{Read: []string{"reader"}},
		}},
	}
	cfg.ApplyDefaults()
	plan := cost.Plan{ScanType: cost.ScanFull, EstimatedRows: 100000, StatsFresh: true}
	app, err := AssembleWithProvider(cfg, &fakeProvider{dialect: mysql.Dialect{}, explainPlan: &plan})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	compiled, err := codegen.Renderer{Dialect: mysql.Dialect{}}.Compile(
		relalg.Scan{Relation: relalg.RelationRef{Name: "users"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := app.Gate.Check(context.Background(), compiled)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Allow {
		t.Fatal("MySQL full scan was not rejected by conservative estimate")
	}
}

func TestAssembleWithProvidersRejectsBareTemplateForMultipleDatasources(t *testing.T) {
	cfg := &config.Config{
		Database: config.DatabaseConfig{Driver: "postgres", DSN: "x"},
		Cost: config.CostConfig{
			RejectTemplates: []string{"SELECT blocked"},
		},
	}
	cfg.ApplyDefaults()
	_, err := AssembleWithProviders(cfg, map[string]Provider{
		"primary": &fakeProvider{dialect: postgres.Dialect{}},
		"replica": &fakeProvider{dialect: postgres.Dialect{}},
	})
	if err == nil || !strings.Contains(err.Error(), "rejectTemplates") ||
		!strings.Contains(err.Error(), "legacy bare SQL") ||
		!strings.Contains(err.Error(), "primary:SELECT blocked") {
		t.Fatalf("error = %v, want bare rejectTemplates migration error", err)
	}
}

func TestAssembleExplainAnalyzeRequiresEveryDatasourceSupport(t *testing.T) {
	cfg := &config.Config{
		Databases: map[string]config.DatabaseConfig{
			"main":    {Driver: "postgres", DSN: "x"},
			"archive": {Driver: "mysql", DSN: "y"},
		},
		Entities: []config.EntityConfig{
			{Name: "users", DataSource: "main"},
			{Name: "events", DataSource: "archive"},
		},
		Cost: config.CostConfig{AQE: config.AQEConfig{
			ExplainAnalyze: true, ReadOnly: true, SampleRate: 1, Timeout: time.Second,
		}},
	}
	cfg.ApplyDefaults()
	main := &fakeAnalyzeProvider{fakeProvider: &fakeProvider{dialect: postgres.Dialect{}}}
	archive := &fakeProvider{dialect: mysql.Dialect{}}
	_, err := AssembleWithProviders(cfg, map[string]Provider{"main": main, "archive": archive})
	if err == nil || !strings.Contains(err.Error(), `datasource "archive"`) {
		t.Fatalf("error = %v, want unsupported archive datasource", err)
	}
}

func TestAssembleWiresAnalyzePolicyPerDatasource(t *testing.T) {
	cfg := &config.Config{
		Database: config.DatabaseConfig{Driver: "postgres", DSN: "x"},
		Entities: []config.EntityConfig{{Name: "users"}},
		Cost: config.CostConfig{AQE: config.AQEConfig{
			ExplainAnalyze: true, ReadOnly: true, SampleRate: 0.5, Timeout: time.Second,
		}},
	}
	cfg.ApplyDefaults()
	provider := &fakeAnalyzeProvider{fakeProvider: &fakeProvider{dialect: postgres.Dialect{}}}
	app, err := AssembleWithProvider(cfg, provider)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if !app.Analyze.Config.Enabled || app.Sources["default"].Analyze.Config.SampleRate != 0.5 {
		t.Fatalf("analyze wiring = %+v / %+v", app.Analyze, app.Sources["default"].Analyze)
	}
}
