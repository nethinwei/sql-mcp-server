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
	{"system permission on role", func(c *Config) {
		c.Roles["ops"] = RoleDefinition{Permissions: []string{"sql:execute@default"}}
	}, "not supported"},
	{"system permission on user", func(c *Config) {
		u := c.Users["Alice"]
		u.Permissions = []string{"admin:read"}
		c.Users["Alice"] = u
	}, "not supported"},
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
	{"duplicate token hash", func(c *Config) {
		c.Users["bob"] = UserConfig{TokenHash: TokenHash("alice-token")}
	}, "collides"},
	{"token hash equals shared token", func(c *Config) {
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
	{"unknown server user", func(c *Config) {
		c.Server.User = "ghost"
	}, "server.user references unknown user"},
	{"disabled server user", func(c *Config) {
		u := c.Users["Alice"]
		u.Disabled = true
		c.Users["Alice"] = u
		c.Server.User = "alice"
	}, "disabled"},
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
