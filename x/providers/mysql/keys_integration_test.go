//go:build integration

package mysql_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/tool"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/providers/mysql"
)

func createMySQLAccounts(t *testing.T, ctx context.Context, prov *mysql.Provider) {
	t.Helper()
	for _, stmt := range []string{
		`CREATE TABLE accounts (
		   id int AUTO_INCREMENT PRIMARY KEY,
		   email varchar(100) NOT NULL UNIQUE,
		   nickname varchar(100) UNIQUE,
		   code varchar(100) NOT NULL,
		   tenant int NOT NULL,
		   total_cents int GENERATED ALWAYS AS (tenant * 100) STORED,
		   UNIQUE KEY accounts_code_prefix (code(3)),
		   UNIQUE KEY accounts_lower_email ((lower(email))))`,
		`INSERT INTO accounts (email, code, tenant) VALUES ('a@x', 'AAAA', 1), ('b@x', 'BBBB', 1)`,
	} {
		if _, err := prov.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}

func TestMySQLKeysColumnsAndConstraintViolations(t *testing.T) {
	prov, cleanup := setupMySQL(t)
	defer cleanup()
	ctx := context.Background()
	createMySQLAccounts(t, ctx, prov)
	assertMySQLAccountKeys(t, ctx, prov)
	assertMySQLAccountWrites(t, ctx, mysqlAccountsApp(t, prov).ToolContext("w"))
}

func assertMySQLAccountKeys(t *testing.T, ctx context.Context, prov *mysql.Provider) {
	t.Helper()
	found, err := prov.Introspector().Discover(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	accounts := found[slices.IndexFunc(found, func(e entity.Entity) bool { return e.Name == "accounts" })]
	reasons := map[string]string{}
	for _, k := range accounts.Keys {
		reasons[k.Name] = k.Reason
	}
	for name, reason := range map[string]string{
		"PRIMARY": "", "email": "", "nickname": entity.KeyNullable,
		"accounts_code_prefix": entity.KeyPrefix, "accounts_lower_email": entity.KeyExpression,
	} {
		if got, ok := reasons[name]; !ok || got != reason {
			t.Errorf("key %s: reason %q (found %v), want %q", name, got, ok, reason)
		}
	}
	for _, a := range accounts.Attributes {
		if a.Name == "id" && (!a.Domain.AutoIncrement || a.Domain.Required()) ||
			a.Name == "total_cents" && a.Domain.Writable() || a.Name == "email" && !a.Domain.Required() {
			t.Errorf("%s = %+v", a.Name, a.Domain)
		}
	}
}

func mysqlAccountsApp(t *testing.T, prov *mysql.Provider) *bootstrap.App {
	t.Helper()
	cfg := &config.Config{
		Database: config.DatabaseConfig{Driver: "mysql", DSN: "ignored"},
		Cost:     config.CostConfig{Enabled: new(false)},
		Entities: []config.EntityConfig{{
			Name: "accounts", PrimaryKey: []string{"id"},
			Fields: []config.FieldConfig{{Name: "id"}, {Name: "email"}, {Name: "nickname"}, {Name: "code"},
				{Name: "tenant"}, {Name: "total_cents"}},
			Roles: config.RoleConfig{Create: []string{"w"}, Update: []string{"w"}},
		}},
	}
	cfg.ApplyDefaults()
	app, err := bootstrap.AssembleWithProvider(cfg, prov)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func assertMySQLAccountWrites(t *testing.T, ctx context.Context, tc tool.Context) {
	t.Helper()
	run := func(tl tool.Tool, input string) (tool.Result, error) {
		return tl.Run(ctx, json.RawMessage(input), tc)
	}
	if _, err := run(tool.UpdateTool{},
		`{"entity":"accounts","set":{"code":"ZZZZ"},"filter":[{"field":"email","op":"eq","value":"a@x"}]}`); err != nil {
		t.Fatalf("an update addressed by a unique key must pass the write guard: %v", err)
	}
	created, err := run(tool.CreateTool{}, `{"entity":"accounts","values":{"email":"n@x","code":"NNNN","tenant":1}}`)
	if err != nil || created.Content[0]["id"] == nil {
		t.Fatalf("create = %+v, %v; want the new row's key", created.Content, err)
	}
	_, err = run(tool.CreateTool{},
		`{"entity":"accounts","values":{"email":"x@x","code":"XXXX","tenant":1,"total_cents":1}}`)
	if !errors.Is(err, tool.ErrInvalidInput) {
		t.Fatalf("writing a generated column: %v", err)
	}
	_, err = run(tool.CreateTool{}, `{"entity":"accounts","values":{"email":"b@x","code":"CCCC","tenant":1}}`)
	denial, ok := tool.DenialFor(err, "d")
	fields, _ := json.Marshal(denial.Constraints["fields"])
	if !ok || denial.Code != tool.CodeConstraintViolation || denial.Constraints["kind"] != "unique" ||
		string(fields) != `["email"]` {
		t.Fatalf("duplicate email: denial = %+v (%v)", denial, err)
	}
}
