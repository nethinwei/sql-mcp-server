package bootstrap

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/budget"
	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
	"github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

func accessTestConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := &config.Config{
		Database: config.DatabaseConfig{Driver: "postgres", DSN: "x"},
		Entities: []config.EntityConfig{{
			Name:         "orders",
			Fields:       []config.FieldConfig{{Name: "id"}, {Name: "amount"}, {Name: "region"}, {Name: "tenant_id"}},
			Roles:        config.RoleConfig{Read: []string{"legacy"}},
			TenantPolicy: config.FilterConfig{"op": "eq", "field": "tenant_id", "value": "${subject.tenant_id}"},
		}},
		Roles: map[string]config.RoleDefinition{
			"analyst": {Grants: []config.GrantConfig{{
				Entity: "orders", Actions: []string{"read"},
				Rows: config.FilterConfig{"op": "eq", "field": "region", "value": "CN"},
			}}},
		},
		Users: map[string]config.UserConfig{
			"alice": {
				TokenHash: config.TokenHash("alice-token"), Roles: []string{"analyst"},
				Subject: map[string]any{"tenant_id": "t1"},
			},
			"bob": {TokenHash: config.TokenHash("bob-token"), Roles: []string{"analyst"}, Disabled: true},
		},
		Budget: config.BudgetConfig{Roles: map[string]config.BudgetLimits{"analyst": {MaxReturnedRows: 5}}},
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestAssembleCompilesUsersRolesAndTenantPolicy(t *testing.T) {
	cfg := accessTestConfig(t)
	app, err := AssembleWithProvider(cfg, &fakeProvider{dialect: postgres.Dialect{}})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	if got := app.UserTokens[config.TokenHash("alice-token")]; got != "alice" {
		t.Fatalf("UserTokens = %v", app.UserTokens)
	}
	if _, ok := app.Users["bob"]; ok || len(app.UserTokens) != 1 {
		t.Fatalf("disabled user must not be servable: %v %v", app.Users, app.UserTokens)
	}
	alice := app.Users["alice"]
	dec, err := app.Authorizer.Authorize(context.Background(), rbac.Request{
		Role: alice.Principal, Subject: alice.Subject, Entity: "orders", Action: entity.ActionRead,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := relalg.And{Preds: []relalg.Predicate{
		relalg.Condition{Field: "tenant_id", Op: relalg.OpEq, Value: "t1"},
		relalg.Condition{Field: "region", Op: relalg.OpEq, Value: "CN"},
	}}
	if !dec.Allowed || !reflect.DeepEqual(dec.RowFilter, want) {
		t.Fatalf("alice decision = %+v", dec)
	}
	dec, _ = app.Authorizer.Authorize(context.Background(), rbac.Request{
		Role: UserPrincipal("bob"), Entity: "orders", Action: entity.ActionRead,
	})
	if dec.Allowed {
		t.Fatal("disabled user must not keep its grants")
	}
	manager, ok := app.Budget.(*budget.MemoryManager)
	if !ok {
		t.Fatalf("budget manager = %T", app.Budget)
	}
	roles, _ := manager.ConfiguredLimits()
	if roles[UserPrincipal("alice")].MaxReturnedRows != 5 {
		t.Fatalf("alice budget must inherit the analyst budget: %+v", roles)
	}
}

func TestUserBudgetsMergeMostPermissive(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Users: map[string]config.UserConfig{
			"both":     {Roles: []string{"small", "large"}},
			"explicit": {Roles: []string{"large"}},
			"open":     {Roles: []string{"small", "unbudgeted"}},
			"none":     {},
		},
		Budget: config.BudgetConfig{
			Roles: map[string]config.BudgetLimits{
				"small": {MaxReturnedRows: 10, MaxExecution: time.Second, MaxScannedRows: 100, MaxConcurrent: 1},
				"large": {MaxReturnedRows: 1000, MaxExecution: time.Minute, MaxEstimatedScannedRows: 50},
			},
			Users: map[string]config.BudgetLimits{"explicit": {MaxReturnedRows: 3}},
		},
	}
	got := userBudgets(cfg)
	both := got[UserPrincipal("both")]
	want := config.BudgetLimits{MaxReturnedRows: 1000, MaxExecution: time.Minute, MaxEstimatedScannedRows: 100}
	if both != want {
		t.Fatalf("both = %+v, want %+v", both, want)
	}
	if got[UserPrincipal("explicit")] != (config.BudgetLimits{MaxReturnedRows: 3}) {
		t.Fatalf("explicit user budget must win: %+v", got[UserPrincipal("explicit")])
	}
	if got[UserPrincipal("open")] != (config.BudgetLimits{}) || got[UserPrincipal("none")] != (config.BudgetLimits{}) {
		t.Fatalf("a role without budget or no role means unlimited: %+v", got)
	}
}

func TestReloadReportsRevokedPrincipals(t *testing.T) {
	t.Parallel()
	users := func(names ...string) *App {
		app := &App{Users: map[string]UserIdentity{}}
		for _, name := range names {
			app.Users[name] = UserIdentity{Principal: UserPrincipal(name)}
		}
		return app
	}
	next := users("alice")
	runtime := NewRuntimeWithBuilder(users("alice", "bob"), func(string) (*App, error) { return next, nil })
	defer runtime.Close()
	var revoked []string
	runtime.OnRevokedPrincipals(func(p []string) { revoked = append(revoked, p...) })
	if err := runtime.Reload("ignored"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(revoked, []string{"user:bob"}) {
		t.Fatalf("revoked = %v", revoked)
	}
	if _, ok := runtime.UserByName("bob"); ok {
		t.Fatal("revoked user must not resolve after reload")
	}
	if id, ok := runtime.UserByName("alice"); !ok || id.Principal != "user:alice" {
		t.Fatalf("kept user = %+v %v", id, ok)
	}
}

func TestCheckHotReloadRejectsRestartRequiredChanges(t *testing.T) {
	t.Parallel()
	base := func() *config.Config {
		cfg := &config.Config{
			Server:   config.ServerConfig{Transport: "http", Addr: ":8080", Auth: config.AuthConfig{Token: "a"}},
			Database: config.DatabaseConfig{Driver: "postgres", DSN: "x"},
			Entities: []config.EntityConfig{{Name: "p", Kind: "procedure",
				MCP: config.MCPFlags{CustomTool: true, TrustedProcedure: true}}},
		}
		cfg.ApplyDefaults()
		return cfg
	}
	if err := CheckHotReload(base(), base()); err != nil {
		t.Fatalf("identical config: %v", err)
	}
	roleOnly := base()
	roleOnly.Server.Role = "other"
	roleOnly.Cost.MaxRows = 7
	if err := CheckHotReload(base(), roleOnly); err != nil {
		t.Fatalf("role and cost changes are hot-reloadable: %v", err)
	}
	cases := map[string]func(*config.Config){
		"transport/address":      func(c *config.Config) { c.Server.Addr = ":9090" },
		"auth/TLS":               func(c *config.Config) { c.Server.Auth.Token = "b" },
		"tool set":               func(c *config.Config) { c.Tools.ReadRecords = !c.Tools.ReadRecords },
		"custom procedure tools": func(c *config.Config) { c.Entities[0].MCP.CustomTool = false },
		"users":                  func(c *config.Config) { c.Users = map[string]config.UserConfig{"alice": {}} },
		"transaction":            func(c *config.Config) { c.Transactions.MaxOpen++ },
	}
	for want, mutate := range cases {
		next := base()
		mutate(next)
		err := CheckHotReload(base(), next)
		if !errors.Is(err, ErrRestartRequired) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", want, err)
		}
	}
}
