package config

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func validConfig() *Config {
	c := &Config{Databases: map[string]DatabaseConfig{"main": {Driver: "postgres", DSN: "x"}}}
	c.ApplyDefaults()
	return c
}

func TestValidateRulesFromTags(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		mutate func(*Config)
		want   string
	}{
		"enum": {func(c *Config) { c.Server.Transport = "tcp" },
			`server.transport is "tcp", want one of stdio, http`},
		"max":            {func(c *Config) { c.Cost.SoftScore = 101 }, "cost.softScore must be at most 100"},
		"NaN":            {func(c *Config) { c.Cost.AQE.SampleRate = math.NaN() }, "cost.aqe.sampleRate must be a number"},
		"duration bound": {func(c *Config) { c.Cost.AQE.Timeout = 6 * time.Second }, "cost.aqe.timeout must be at most 5s"},
		"negative budget": {func(c *Config) { c.Budget.Tenants = map[string]BudgetLimits{"t": {MaxExecution: -1}} },
			"budget.tenants.t.maxExecution must be at least 0"},
		"entity kind": {func(c *Config) { c.Entities = []EntityConfig{{Name: "a", DataSource: "main", Kind: "table2"}} },
			`entities[0].kind is "table2"`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := validConfig()
			tc.mutate(c)
			err := c.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want %q", err, tc.want)
			}
		})
	}
	// An unset legacy database is not checked against its required fields.
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	// List elements are checked even when zero.
	c := validConfig()
	c.Roles = map[string]RoleDefinition{"r": {Grants: []GrantConfig{{}}}}
	if err := c.validateRules(); err == nil || !strings.Contains(err.Error(), "roles.r.grants[0].entity is required") {
		t.Fatalf("zero grant: %v", err)
	}
}

// Every schema tag on a configuration type must parse.
// The prepared statement cache works without the result cache, whose entry
// limits only matter (and only get defaults) when it is enabled.
func TestPreparedCacheWithoutResultCache(t *testing.T) {
	t.Parallel()
	c := &Config{Databases: map[string]DatabaseConfig{"main": {Driver: "postgres", DSN: "x"}},
		Cache: CacheConfig{PreparedMaxSize: 128}}
	c.ApplyDefaults()
	if err := c.Validate(); err != nil {
		t.Fatalf("prepared cache alone: %v", err)
	}
	c.Cache.Enabled, c.Cache.MaxEntryRows = true, 0
	if err := c.Validate(); err == nil {
		t.Fatal("an enabled result cache still needs positive entry limits")
	}
}

func TestSchemaTagsParse(t *testing.T) {
	t.Parallel()
	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type)
	walk = func(typ reflect.Type) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Map {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || seen[typ] {
			return
		}
		seen[typ] = true
		for i := range typ.NumField() {
			f := typ.Field(i)
			if YAMLName(f) == "" {
				continue
			}
			if _, err := ParseRule(f.Tag.Get("schema")); err != nil {
				t.Errorf("%s.%s: %v", typ.Name(), f.Name, err)
			}
			walk(f.Type)
		}
	}
	walk(reflect.TypeFor[Config]())
}

func TestRestartFieldChanges(t *testing.T) {
	t.Parallel()
	old, next := validConfig(), validConfig()
	next.Server.Auth.TrustedProxyCIDRs = []string{}
	next.Cost.MaxRows = 7
	if got := RestartFieldChanges(old, next); len(got) != 0 {
		t.Fatalf("empty list and hot-reloadable changes reported: %v", got)
	}
	next.Server.Addr = ":9"
	next.Tools.DeleteRecord = !next.Tools.DeleteRecord
	next.Transactions.MaxOpen++
	got := strings.Join(RestartFieldChanges(old, next), " ")
	if got != "server.addr tools transactions.maxOpen" {
		t.Fatalf("changes = %q", got)
	}
}

// Switching on any optional block with nothing else set must load: every
// field a rule requires once the block is on has to get a default. A rule
// that needs user input (audit.path) belongs to the cases it lists instead.
func TestOptionalBlocksLoadWithDefaults(t *testing.T) {
	t.Parallel()
	on := true
	for name, mutate := range map[string]func(*Config){
		"result cache":      func(c *Config) { c.Cache.Enabled = true },
		"prepared cache":    func(c *Config) { c.Cache.PreparedMaxSize = 64 },
		"rate limit rps":    func(c *Config) { c.RateLimit.RPS = 10 },
		"aqe sampling":      func(c *Config) { c.Cost.AQE.SampleRate = 0.5 },
		"http transport":    func(c *Config) { c.Server.Transport = "http" },
		"explicit tools":    func(c *Config) { c.Tools = ExplicitToolFlags(ToolFlags{ReadRecords: true}) },
		"cost switched on":  func(c *Config) { c.Cost.Enabled = &on },
		"budget role entry": func(c *Config) { c.Budget.Roles = map[string]BudgetLimits{"reader": {}} },
		"entity":            func(c *Config) { c.Entities = []EntityConfig{{Name: "orders", DataSource: "main"}} },
		"role with a grant": func(c *Config) {
			c.Entities = []EntityConfig{{Name: "orders", DataSource: "main", Fields: []FieldConfig{{Name: "id"}}}}
			c.Roles = map[string]RoleDefinition{"r": {Grants: []GrantConfig{{Entity: "orders", Actions: []string{"read"}}}}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := &Config{Databases: map[string]DatabaseConfig{"main": {Driver: "postgres", DSN: "x"}}}
			mutate(c)
			c.ApplyDefaults()
			if err := c.Validate(); err != nil {
				t.Fatalf("%s needs a default: %v", name, err)
			}
		})
	}
}
