package config

import (
	"strings"
	"testing"
)

func accessConfig() *Config {
	return &Config{
		Database: DatabaseConfig{Driver: "postgres", DSN: "x"},
		Entities: []EntityConfig{{
			Name:         "orders",
			Fields:       []FieldConfig{{Name: "id"}, {Name: "amount"}, {Name: "region"}, {Name: "secret", Exclude: true}},
			Roles:        RoleConfig{Read: []string{"legacy"}},
			TenantPolicy: FilterConfig{"op": "eq", "field": "tenant_id", "value": "${subject.tenant_id}"},
		}},
		Roles: map[string]RoleDefinition{
			"Analyst": {Grants: []GrantConfig{{
				Entity:  "orders",
				Actions: []string{"Read", "aggregate"},
				Fields:  &FieldACLConfig{Read: []string{"id", "amount"}},
				Rows:    FilterConfig{"op": "eq", "field": "region", "value": "CN"},
			}}},
		},
		Users: map[string]UserConfig{
			"Alice": {
				TokenHash: strings.ToUpper(TokenHash("alice-token")),
				Roles:     []string{"analyst", "LEGACY"},
				Subject:   map[string]any{"tenant_id": "t1"},
				Grants:    []GrantConfig{{Entity: "orders", Actions: []string{"read"}}},
			},
		},
		Budget: BudgetConfig{Users: map[string]BudgetLimits{"ALICE": {MaxReturnedRows: 10}}},
	}
}

func TestValidateAccessNormalizesNames(t *testing.T) {
	t.Parallel()
	cfg := accessConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	analyst, ok := cfg.Roles["analyst"]
	if !ok {
		t.Fatalf("role not normalized: %v", cfg.Roles)
	}
	if got := analyst.Grants[0].Actions; got[0] != "read" {
		t.Fatalf("actions not normalized: %v", got)
	}
	alice, ok := cfg.Users["alice"]
	if !ok {
		t.Fatalf("user not normalized: %v", cfg.Users)
	}
	if alice.TokenHash != TokenHash("alice-token") || alice.Roles[1] != "legacy" {
		t.Fatalf("user fields not normalized: %+v", alice)
	}
	if _, ok := cfg.Budget.Users["alice"]; !ok {
		t.Fatalf("budget user not normalized: %v", cfg.Budget.Users)
	}
}

// accessRejectCases lists invalid access configurations and the error each
// must produce.
var accessRejectCases = []struct {
	name   string
	mutate func(*Config)
	want   string
}{
	{"bad role name", func(c *Config) {
		c.Roles["bad:role"] = RoleDefinition{}
	}, "must match"},
	{"bad user name", func(c *Config) {
		c.Users["-bob"] = UserConfig{}
	}, "must match"},
	{"colliding role", func(c *Config) {
		c.Roles["analyst"] = RoleDefinition{}
	}, "colliding role"},
	{"unknown role", func(c *Config) {
		u := c.Users["Alice"]
		u.Roles = []string{"ghost"}
		c.Users["Alice"] = u
	}, "unknown role"},
	{"duplicate user role", func(c *Config) {
		u := c.Users["Alice"]
		u.Roles = []string{"analyst", "Analyst"}
		c.Users["Alice"] = u
	}, "twice"},
	{"unsupported system permission on role", func(c *Config) {
		c.Roles["ops"] = RoleDefinition{Permissions: []string{"admin:read"}}
	}, "not supported"},
	{"unsupported system permission on user", func(c *Config) {
		u := c.Users["Alice"]
		u.Permissions = []string{"admin:read"}
		c.Users["Alice"] = u
	}, "not supported"},
	{"sql:execute on an unknown datasource", func(c *Config) {
		c.Roles["ops"] = RoleDefinition{Permissions: []string{"sql:execute@ghost"}}
	}, "unknown datasource"},
	{"sql:execute without a datasource", func(c *Config) {
		c.Roles["ops"] = RoleDefinition{Permissions: []string{"sql:execute@"}}
	}, "not supported"},
	{"duplicate system permission", func(c *Config) {
		u := c.Users["Alice"]
		u.Permissions = []string{"sql:execute@default", " sql:execute@default"}
		c.Users["Alice"] = u
	}, "twice"},
	{"execute_sql without audit", func(c *Config) {
		c.Tools = ToolFlags{ExecuteSQL: true, present: true}
	}, "requires audit.enabled"},
	{"sqlEscape on an unknown connection", func(c *Config) {
		c.Database.SQLEscape = SQLEscapeConfig{Connection: "ops"}
	}, "unknown connection"},
	{"malformed token hash", func(c *Config) {
		u := c.Users["Alice"]
		u.TokenHash = "md5:abc"
		c.Users["Alice"] = u
	}, "must match ^sha256:"},
	{"non hex token hash", func(c *Config) {
		u := c.Users["Alice"]
		u.TokenHash = TokenHashPrefix + strings.Repeat("z", 64)
		c.Users["Alice"] = u
	}, "must match ^sha256:"},
	{"malformed certificate subject", func(c *Config) {
		u := c.Users["Alice"]
		u.CertSubjects = []string{"subject:alice"}
		c.Users["Alice"] = u
	}, "must match"},
	{"certificate subject of two users", func(c *Config) {
		u := c.Users["Alice"]
		u.CertSubjects = []string{"dns:agent.example.org"}
		c.Users["Alice"] = u
		c.Users["bob"] = UserConfig{CertSubjects: []string{"DNS:Agent.example.org"}}
	}, "names users"},
	{"certificate subject of a trusted proxy", func(c *Config) {
		u := c.Users["Alice"]
		u.CertSubjects = []string{"cn:gateway"}
		c.Users["Alice"] = u
		c.Server.Auth.TLS.ClientCA = "ca.pem"
		c.Server.Auth.TrustedProxyCertSubjects = []string{"gateway"}
	}, "trusted proxy"},
	{"unknown client identity", func(c *Config) { c.Server.Auth.TLS.ClientIdentity = "anyone" }, "want one of"},
	{"duplicate token hash", func(c *Config) {
		c.Users["bob"] = UserConfig{TokenHash: TokenHash("alice-token")}
	}, "collides"},
	{"token hash equals shared token", func(c *Config) {
		c.Server.Addr = "127.0.0.1:8080"
		c.Server.Auth.Token = "alice-token"
	}, "collides with server.auth.token"},
	{"grant unknown entity", func(c *Config) {
		c.Roles["x"] = RoleDefinition{Grants: []GrantConfig{{Entity: "ghost", Actions: []string{"read"}}}}
	}, "unknown entity"},
	{"grant without actions", func(c *Config) {
		c.Roles["x"] = RoleDefinition{Grants: []GrantConfig{{Entity: "orders"}}}
	}, "actions needs at least 1 items"},
	{"grant unknown action", func(c *Config) {
		c.Roles["x"] = RoleDefinition{Grants: []GrantConfig{{Entity: "orders", Actions: []string{"drop"}}}}
	}, `is "drop", want one of`},
	{"grant duplicate action", func(c *Config) {
		c.Roles["x"] = RoleDefinition{Grants: []GrantConfig{{Entity: "orders", Actions: []string{"read", "READ"}}}}
	}, "twice"},
	{"grant excluded field", func(c *Config) {
		c.Roles["x"] = RoleDefinition{Grants: []GrantConfig{{
			Entity: "orders", Actions: []string{"read"}, Fields: &FieldACLConfig{Read: []string{"secret"}},
		}}}
	}, "unknown or excluded field"},
	{"grant invalid rows", func(c *Config) {
		c.Roles["x"] = RoleDefinition{Grants: []GrantConfig{{
			Entity: "orders", Actions: []string{"read"}, Rows: FilterConfig{"op": "bogus"},
		}}}
	}, "rows"},
	{"invalid tenant policy", func(c *Config) {
		c.Entities[0].TenantPolicy = FilterConfig{"nope": true}
	}, "tenant policy"},
	{"legacy role with colon", func(c *Config) {
		c.Entities[0].Roles.Read = []string{"user:alice"}
	}, "must not contain ':'"},
	{"empty entity role", func(c *Config) {
		c.Entities[0].Roles.Read = []string{" "}
	}, "a role must not be empty"},
	{"server role naming a user", func(c *Config) {
		c.Server.Role = "user:alice"
	}, "server.role"},
	{"unknown server user", func(c *Config) {
		c.Server.User = "ghost"
	}, "server.user references unknown user"},
	{"disabled server user", func(c *Config) {
		u := c.Users["Alice"]
		u.Disabled = true
		c.Users["Alice"] = u
		c.Server.User = "alice"
	}, "disabled"},
	{"unknown budget role", func(c *Config) {
		c.Budget.Roles = map[string]BudgetLimits{"ghost": {}}
	}, "budget references unknown role"},
	{"unknown budget user", func(c *Config) {
		c.Budget.Users["ghost"] = BudgetLimits{}
	}, "budget references unknown user"},
	{"negative budget user", func(c *Config) {
		c.Budget.Users["ALICE"] = BudgetLimits{MaxReturnedRows: -1}
	}, "must be at least 0"},
}

func TestValidateAccessRejectsInvalidConfig(t *testing.T) {
	t.Parallel()
	for _, tc := range accessRejectCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := accessConfig()
			tc.mutate(cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestValidateAccessAllowsLegacyColonRolesWithoutUsers(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Database: DatabaseConfig{Driver: "postgres", DSN: "x"},
		Entities: []EntityConfig{{Name: "orders", Roles: RoleConfig{Read: []string{"team:ops"}}}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("legacy config without users must stay valid: %v", err)
	}
}

func TestValidateRejectsReservedStoreTables(t *testing.T) {
	t.Parallel()
	for _, e := range []EntityConfig{{Name: "smcp_revisions"}, {Name: "revs", Source: "SMCP_Revisions"}} {
		cfg := &Config{Database: DatabaseConfig{Driver: "postgres", DSN: "x"}, Entities: []EntityConfig{e}}
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "reserved store prefix") {
			t.Errorf("entity %+v: err = %v", e, err)
		}
	}
	cfg := &Config{Database: DatabaseConfig{Driver: "postgres", DSN: "x"},
		Entities: []EntityConfig{{Name: "smcp_view", Source: "orders"}}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("only the physical source is reserved: %v", err)
	}
}

// sql:execute@<datasource> is the one system permission this version
// accepts, on roles and users alike, whether or not the datasource admits the
// escape hatch (that is the datasource's own condition).
func TestValidateAcceptsSQLExecutePermissions(t *testing.T) {
	t.Parallel()
	cfg := accessConfig()
	cfg.Roles["ops"] = RoleDefinition{Permissions: []string{" sql:execute@default "}}
	alice := cfg.Users["Alice"]
	alice.Permissions = []string{"sql:execute@default"}
	cfg.Users["Alice"] = alice
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := cfg.Roles["ops"].Permissions; len(got) != 1 || got[0] != "sql:execute@default" {
		t.Fatalf("permissions not trimmed: %q", got)
	}
	if ds, ok := ParseSQLExecute("sql:execute@crm"); !ok || ds != "crm" {
		t.Fatalf("ParseSQLExecute = %q, %v", ds, ok)
	}
	if _, ok := ParseSQLExecute("admin:read"); ok {
		t.Fatal("admin:read parsed as sql:execute")
	}
}

// The escape hatch is validated with its audit: enabling execute_sql needs
// an audit sink, and a datasource's sqlEscape names one of its connections.
func TestValidateSQLEscape(t *testing.T) {
	t.Parallel()
	cfg := accessConfig()
	cfg.Tools = ToolFlags{ExecuteSQL: true, present: true}
	cfg.Audit = AuditConfig{Enabled: true, Path: "audit.jsonl"}
	cfg.Database.SQLEscape = SQLEscapeConfig{Connection: DefaultConnection}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !cfg.Databases["default"].SQLEscape.Enabled() {
		t.Fatal("sqlEscape lost in the default datasource")
	}
	if cfg.Cost.SQLEscape.Estimate != SQLEscapeEstimateRequired || !cfg.Cost.SQLEscape.EstimateRequired() {
		t.Fatalf("estimate default = %q", cfg.Cost.SQLEscape.Estimate)
	}
	cfg.Cost.SQLEscape.Estimate = "sometimes"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "sqlEscape.estimate") {
		t.Fatalf("estimate enum not checked: %v", err)
	}
	cfg.Cost.SQLEscape.Estimate = SQLEscapeEstimateSkip
	if err := cfg.Validate(); err != nil || cfg.Cost.SQLEscape.EstimateRequired() {
		t.Fatalf("skip: %v", err)
	}
	// Without cost there is no EXPLAIN: a required estimate cannot hold.
	off := false
	cfg.Cost.Enabled = &off
	if err := cfg.Validate(); err != nil {
		t.Fatalf("skip without cost: %v", err)
	}
	cfg.Cost.SQLEscape.Estimate = SQLEscapeEstimateRequired
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "estimate: skip") {
		t.Fatalf("a required estimate without cost: %v", err)
	}
	old, next := accessConfig(), accessConfig()
	old.ApplyDefaults()
	next.Databases = map[string]DatabaseConfig{"default": {Driver: "postgres", DSN: "x",
		SQLEscape: SQLEscapeConfig{Connection: DefaultConnection}}}
	next.ApplyDefaults()
	if got := RestartFieldChanges(old, next); len(got) != 0 {
		t.Fatalf("sqlEscape.connection hot-reloads like the other connection settings: %v", got)
	}
}
