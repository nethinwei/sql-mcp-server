//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/core/tool"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	pgprov "github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

// setupPGAccounts starts PostgreSQL with a read-only and a read-write role and
// returns their DSNs.
func setupPGAccounts(t *testing.T) (ro, rw string, cleanup func()) {
	t.Helper()
	ctx := context.Background()
	container, err := postgres.Run(ctx, "postgres:16-alpine", postgres.WithDatabase("test"),
		postgres.WithUsername("test"), postgres.WithPassword("test"), postgres.BasicWaitStrategies())
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgprov.New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close() }()
	for _, stmt := range []string{
		`CREATE TABLE notes (id serial PRIMARY KEY, body text)`,
		`CREATE VIEW whoami AS SELECT 1 AS id, current_user::text AS who`,
		`CREATE ROLE app_ro LOGIN PASSWORD 'ro'`, `CREATE ROLE app_rw LOGIN PASSWORD 'rw'`,
		`GRANT SELECT ON notes, whoami TO app_ro`,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON notes TO app_rw`, `GRANT SELECT ON whoami TO app_rw`,
		`GRANT USAGE ON SEQUENCE notes_id_seq TO app_rw`,
		`ALTER ROLE app_ro SET default_transaction_read_only = on`,
		`CREATE TABLE salaries (id integer PRIMARY KEY, name text, amount integer)`,
		`GRANT SELECT (id, name) ON salaries TO app_ro`,
	} {
		if _, err := admin.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	return strings.Replace(dsn, "test:test@", "app_ro:ro@", 1), strings.Replace(dsn, "test:test@", "app_rw:rw@", 1),
		func() { _ = container.Terminate(context.Background()) }
}

func TestPGRoutesActionsAcrossAccounts(t *testing.T) {
	ro, rw, cleanup := setupPGAccounts(t)
	defer cleanup()
	ctx := context.Background()
	roles := config.RoleConfig{Read: []string{"u"}, Create: []string{"u"}}
	cfg := &config.Config{
		Databases: map[string]config.DatabaseConfig{"main": {
			Driver:         "postgres",
			Connections:    config.Connections{"ro": {DSN: ro, Pooler: "transaction"}, "rw": {DSN: rw}},
			Routing:        config.RoutingConfig{Read: "ro", Write: "rw", Execute: "rw"},
			ReadAfterWrite: time.Minute,
		}},
		Cost: config.CostConfig{Enabled: new(false)},
		Entities: []config.EntityConfig{
			{Name: "notes", DataSource: "main", Fields: []config.FieldConfig{{Name: "id"}, {Name: "body"}}, Roles: roles},
			{Name: "whoami", DataSource: "main", Kind: "view", Fields: []config.FieldConfig{{Name: "id"}, {Name: "who"}},
				Roles: roles},
		},
	}
	cfg.ApplyDefaults()
	app, err := bootstrap.Assemble(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	who := func(session string) string {
		tc := app.ToolContext("u")
		tc.Session = session
		res, err := tool.ReadTool{}.Run(ctx, json.RawMessage(`{"entity":"whoami"}`), tc)
		if err != nil {
			t.Fatal(err)
		}
		return res.Content[0]["who"].(string)
	}
	if got := who("s1"); got != "app_ro" {
		t.Fatalf("reads use %s, want app_ro", got)
	}
	tc := app.ToolContext("u")
	tc.Session = "s1"
	note := json.RawMessage(`{"entity":"notes","values":{"body":"a"}}`)
	if _, err := (tool.CreateTool{}).Run(ctx, note, tc); err != nil {
		t.Fatalf("the write must go through the read-write account: %v", err)
	}
	if got := who("s1"); got != "app_rw" {
		t.Fatalf("after its write a session reads through %s, want app_rw", got)
	}
	if got := who("s2"); got != "app_ro" {
		t.Fatalf("another session reads through %s, want app_ro", got)
	}
}

func TestPGCapabilitiesAndRefusedWrites(t *testing.T) {
	ro, _, cleanup := setupPGAccounts(t)
	defer cleanup()
	ctx := context.Background()
	roles := config.RoleConfig{Read: []string{"u"}, Create: []string{"u"}}
	cfg := &config.Config{
		Database: config.DatabaseConfig{Driver: "postgres", DSN: ro},
		Cost:     config.CostConfig{Enabled: new(false)},
		Entities: []config.EntityConfig{
			{Name: "notes", Fields: []config.FieldConfig{{Name: "id"}, {Name: "body"}}, Roles: roles},
			{Name: "salaries", Fields: []config.FieldConfig{{Name: "id"}, {Name: "name"}}, Roles: roles},
		},
	}
	cfg.ApplyDefaults()
	app, err := bootstrap.Assemble(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	capabilities, err := app.WaitCapabilities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	notes := capabilities["default.notes"]
	if notes[entity.ActionRead].Privilege != introspect.PrivilegeGranted ||
		notes[entity.ActionCreate].Privilege != introspect.PrivilegeDenied ||
		!strings.Contains(notes[entity.ActionCreate].Reason, "read-only") {
		t.Fatalf("notes capabilities = %+v", notes)
	}
	if read := capabilities["default.salaries"][entity.ActionRead]; read.Privilege != introspect.PrivilegeGranted ||
		!slices.Equal(read.Columns, []string{"id", "name"}) {
		t.Fatalf("salaries read = %+v, want the column-level grant", read)
	}
	if warnings := bootstrap.CapabilityWarnings(cfg, capabilities); len(warnings) != 2 {
		t.Fatalf("warnings = %v, want the two creates", warnings)
	}
	_, err = tool.CreateTool{}.Run(ctx, json.RawMessage(`{"entity":"notes","values":{"body":"a"}}`), app.ToolContext("u"))
	denial, ok := tool.DenialFor(err, "d")
	if !ok || denial.Code != tool.CodeDatasourceForbidden {
		t.Fatalf("a write on a read-only account: denial = %+v (%v)", denial, err)
	}
}
