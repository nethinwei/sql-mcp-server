//go:build integration

package postgres_test

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
	pgprov "github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

func createPGAccounts(t *testing.T, ctx context.Context, prov *pgprov.Provider) {
	t.Helper()
	for _, stmt := range []string{
		`CREATE TABLE accounts (
		   id serial PRIMARY KEY,
		   email text NOT NULL UNIQUE,
		   nickname text UNIQUE,
		   code text NOT NULL,
		   tenant integer NOT NULL,
		   total_cents integer GENERATED ALWAYS AS (tenant * 100) STORED,
		   deleted boolean NOT NULL DEFAULT false)`,
		`CREATE UNIQUE INDEX accounts_code_live ON accounts (code) WHERE NOT deleted`,
		`CREATE UNIQUE INDEX accounts_lower_email ON accounts (lower(email))`,
		`CREATE UNIQUE INDEX accounts_tenant_code ON accounts (tenant, code) INCLUDE (nickname)`,
		`INSERT INTO accounts (email, nickname, code, tenant) VALUES ('a@x', NULL, 'A', 1), ('b@x', NULL, 'B', 1)`,
	} {
		if _, err := prov.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}

func TestPGDiscoversKeysAndColumnAttributes(t *testing.T) {
	prov, cleanup := setupPG(t)
	defer cleanup()
	ctx := context.Background()
	createPGAccounts(t, ctx, prov)
	found, err := prov.Introspector().Discover(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(found, func(e entity.Entity) bool { return e.Name == "accounts" })
	accounts := found[i]
	reasons := map[string]string{}
	for _, k := range accounts.Keys {
		reasons[k.Name] = k.Reason
	}
	want := map[string]string{
		"accounts_pkey": "", "accounts_email_key": "", "accounts_nickname_key": entity.KeyNullable,
		"accounts_code_live": entity.KeyPartial, "accounts_lower_email": entity.KeyExpression,
		"accounts_tenant_code": "", // the nullable INCLUDE column is not part of the key
	}
	for name, reason := range want {
		if got, ok := reasons[name]; !ok || got != reason {
			t.Errorf("key %s: reason %q (found %v), want %q", name, got, ok, reason)
		}
	}
	if keys := accounts.IdentityKeys(true, false); len(keys) != 3 || keys[0][0] != "id" || keys[1][0] != "email" ||
		!slices.Equal(keys[2], []string{"tenant", "code"}) {
		t.Errorf("identity keys = %v", keys)
	}
	domains := map[string]entity.Domain{}
	for _, a := range accounts.Attributes {
		domains[a.Name] = a.Domain
	}
	if d := domains["id"]; !d.AutoIncrement || !d.Default || d.Required() {
		t.Errorf("id = %+v", d)
	}
	if d := domains["total_cents"]; !d.Generated || d.Writable() {
		t.Errorf("total_cents = %+v", d)
	}
	if !domains["email"].Required() || domains["deleted"].Required() {
		t.Errorf("email = %+v, deleted = %+v", domains["email"], domains["deleted"])
	}
}

func TestPGWritesByUniqueKeyAndConstraintViolations(t *testing.T) {
	prov, cleanup := setupPG(t)
	defer cleanup()
	ctx := context.Background()
	createPGAccounts(t, ctx, prov)
	tc := pgAccountsApp(t, prov).ToolContext("w")
	run := func(t tool.Tool, input map[string]any) error {
		raw, _ := json.Marshal(input)
		_, err := t.Run(ctx, raw, tc)
		return err
	}
	update := func(field string, value any) map[string]any {
		return map[string]any{"entity": "accounts", "set": map[string]any{"code": "Z"},
			"filter": []map[string]any{{"field": field, "op": "eq", "value": value}}}
	}
	if err := run(tool.UpdateTool{}, update("email", "a@x")); err != nil {
		t.Fatalf("an update addressed by a unique key must pass the write guard: %v", err)
	}
	if err := run(tool.UpdateTool{}, update("nickname", "n")); err == nil {
		t.Fatal("a nullable unique key must not admit an update")
	}
	if err := run(tool.UpdateTool{}, map[string]any{"entity": "accounts", "set": map[string]any{"total_cents": 1},
		"filter": []map[string]any{{"field": "id", "op": "eq", "value": 1}}}); !errors.Is(err, tool.ErrInvalidInput) {
		t.Fatalf("writing a generated column: %v", err)
	}
	for input, want := range map[string][2]string{
		`{"entity":"accounts","values":{"email":"b@x","code":"C","tenant":1}}`: {"unique", `["email"]`},
		`{"entity":"accounts","values":{"email":"c@x","tenant":1}}`:            {"not_null", `["code"]`},
	} {
		_, err := tool.CreateTool{}.Run(ctx, json.RawMessage(input), tc)
		denial, ok := tool.DenialFor(err, "d")
		fields, _ := json.Marshal(denial.Constraints["fields"])
		if !ok || denial.Code != tool.CodeConstraintViolation || denial.Constraints["kind"] != want[0] ||
			string(fields) != want[1] {
			t.Errorf("%s: denial = %+v (%v)", input, denial, err)
		}
	}
}

func pgAccountsApp(t *testing.T, prov *pgprov.Provider) *bootstrap.App {
	t.Helper()
	cfg := &config.Config{
		Database: config.DatabaseConfig{Driver: "postgres", DSN: "ignored"},
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
