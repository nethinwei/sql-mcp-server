package config

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestApplyDefaultsTools(t *testing.T) {
	t.Parallel()
	c := &Config{Database: DatabaseConfig{Driver: "postgres", DSN: "x"}}
	c.ApplyDefaults()
	if !c.Tools.ReadRecords {
		t.Error("ReadRecords should default on")
	}
	if c.Tools.DeleteRecord {
		t.Error("DeleteRecord should default off")
	}
}

func TestApplyDefaultsRespectsExplicitTools(t *testing.T) {
	t.Parallel()
	c := &Config{
		Database: DatabaseConfig{Driver: "postgres", DSN: "x"},
		Tools:    ToolFlags{ReadRecords: true}, // explicit, non-zero
	}
	c.ApplyDefaults()
	if c.Tools.CreateRecord {
		t.Error("explicit Tools section should not inherit defaults")
	}
}

func TestApplyDefaultsMCP(t *testing.T) {
	t.Parallel()
	c := &Config{
		Database: DatabaseConfig{Driver: "mysql", DSN: "x"},
		Entities: []EntityConfig{{Name: "users"}},
	}
	c.ApplyDefaults()
	if !c.Entities[0].MCP.DMLTools {
		t.Error("entity MCP.DMLTools should default true")
	}
}

func TestApplyDefaultsPreservesExplicitMCPFalse(t *testing.T) {
	t.Parallel()
	var c Config
	if err := yaml.Unmarshal([]byte(`
database: {driver: postgres, dsn: x}
entities:
  - name: hidden
    mcp:
      dmlTools: false
`), &c); err != nil {
		t.Fatal(err)
	}
	c.ApplyDefaults()
	if c.Entities[0].MCP.DMLTools {
		t.Error("explicit mcp.dmlTools false must be preserved")
	}
}

func TestApplyDefaultsPreservesJSONAndProgrammaticMCPFalse(t *testing.T) {
	t.Parallel()
	var fromJSON Config
	if err := json.Unmarshal([]byte(`{"database":{"driver":"postgres","dsn":"x"},"entities":[{"name":"hidden","mcp":{"dmlTools":false}}]}`), &fromJSON); err != nil {
		t.Fatal(err)
	}
	fromJSON.ApplyDefaults()
	if fromJSON.Entities[0].MCP.DMLTools {
		t.Fatal("JSON explicit dmlTools false must be preserved")
	}
	programmatic := Config{
		Database: DatabaseConfig{Driver: "postgres", DSN: "x"},
		Entities: []EntityConfig{{Name: "hidden", MCP: MCPFlagsWithDMLTools(false)}},
	}
	programmatic.ApplyDefaults()
	if programmatic.Entities[0].MCP.DMLTools {
		t.Fatal("programmatic explicit dmlTools false must be preserved")
	}
}

func TestApplyDefaultsPreservesAllFalseToolsNode(t *testing.T) {
	t.Parallel()
	for name, decode := range map[string]func(*Config) error{
		"yaml": func(cfg *Config) error { return yaml.Unmarshal([]byte("tools: {}"), cfg) },
		"json": func(cfg *Config) error { return json.Unmarshal([]byte(`{"tools":{}}`), cfg) },
	} {
		t.Run(name, func(t *testing.T) {
			var cfg Config
			if err := decode(&cfg); err != nil {
				t.Fatal(err)
			}
			cfg.ApplyDefaults()
			if cfg.Tools != (ToolFlags{present: true}) {
				t.Fatalf("explicit all-false tools changed: %+v", cfg.Tools)
			}
		})
	}
}

func TestApplyDefaultsCostThresholds(t *testing.T) {
	t.Parallel()
	c := &Config{Database: DatabaseConfig{Driver: "postgres", DSN: "x"}, Cost: CostConfig{Enabled: Bool(true)}}
	c.ApplyDefaults()
	if c.Cost.HardScore != 40 || c.Cost.SoftScore != 60 || c.Cost.MaxRows != 10000 {
		t.Fatalf("expected default thresholds, got %+v", c.Cost)
	}
	if !c.Cost.WhitelistPKPoint {
		t.Error("WhitelistPKPoint should default true when cost enabled")
	}
}

func TestApplyDefaultsCostFieldsIndependently(t *testing.T) {
	t.Parallel()
	var cfg Config
	if err := yaml.Unmarshal([]byte("cost: {softScore: 70}"), &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.ApplyDefaults()
	if cfg.Cost.SoftScore != 70 || cfg.Cost.HardScore != 40 || cfg.Cost.MaxRows != 10000 ||
		cfg.Cost.QueryTimeout != 30*time.Second || !cfg.Cost.WhitelistPKPoint {
		t.Fatalf("partial cost defaults were lost: %+v", cfg.Cost)
	}
	var explicit Config
	if err := json.Unmarshal([]byte(`{"cost":{"hardScore":0,"whitelistPKPoint":false}}`), &explicit); err != nil {
		t.Fatal(err)
	}
	explicit.ApplyDefaults()
	if explicit.Cost.HardScore != 0 || explicit.Cost.WhitelistPKPoint {
		t.Fatalf("explicit zero/false cost fields changed: %+v", explicit.Cost)
	}
}

func TestApplyDefaultsCostEnabledTriState(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		yaml string
		want bool
	}{
		{"omitted", "cost: {}", true},
		{"explicit false", "cost: {enabled: false}", false},
		{"explicit true", "cost: {enabled: true}", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var c Config
			if err := yaml.Unmarshal([]byte(tc.yaml), &c); err != nil {
				t.Fatal(err)
			}
			c.ApplyDefaults()
			if c.Cost.EnabledOrDefault() != tc.want {
				t.Fatalf("Enabled = %v, want %v", c.Cost.EnabledOrDefault(), tc.want)
			}
			if c.Cost.QueryTimeout != 30*time.Second {
				t.Fatalf("QueryTimeout = %v, want 30s", c.Cost.QueryTimeout)
			}
		})
	}
}

func TestApplyDefaultsCacheTTL(t *testing.T) {
	t.Parallel()
	c := &Config{Database: DatabaseConfig{Driver: "postgres", DSN: "x"}, Cache: CacheConfig{Enabled: true}}
	c.ApplyDefaults()
	if c.Cache.TTL != 30*time.Second {
		t.Fatalf("got %v, want 30s", c.Cache.TTL)
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		cfg     *Config
		wantErr error
	}{
		{"valid", &Config{Database: DatabaseConfig{Driver: "oceanbase", DSN: "x"}, Entities: []EntityConfig{{Name: "u"}}}, nil},
		{"bad driver", &Config{Database: DatabaseConfig{Driver: "oracle", DSN: "x"}}, ErrInvalidDriver},
		{"empty dsn", &Config{Database: DatabaseConfig{Driver: "postgres"}}, ErrEmptyDSN},
		{"empty entity name", &Config{Database: DatabaseConfig{Driver: "postgres", DSN: "x"}, Entities: []EntityConfig{{Name: ""}}}, ErrEmptyEntityName},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.cfg.Validate()
			if c.wantErr == nil {
				if err != nil {
					t.Fatalf("got %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("got %v, want %v", err, c.wantErr)
			}
		})
	}
}

func TestValidateSecurityConstraints(t *testing.T) {
	t.Parallel()
	valid := func() *Config {
		return &Config{
			Database: DatabaseConfig{Driver: "postgres", DSN: "x"},
			Cost:     CostConfig{SoftScore: 60, HardScore: 40},
			Entities: []EntityConfig{{Name: "users"}},
		}
	}
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"soft below zero", func(c *Config) { c.Cost.SoftScore = -1 }},
		{"soft above 100", func(c *Config) { c.Cost.SoftScore = 101 }},
		{"hard below zero", func(c *Config) { c.Cost.HardScore = -1 }},
		{"hard above 100", func(c *Config) { c.Cost.HardScore = 101 }},
		{"soft below hard", func(c *Config) { c.Cost.SoftScore = 39 }},
		{"audit path missing", func(c *Config) { c.Audit.Enabled = true }},
		{"negative io pool", func(c *Config) { c.RateLimit.IOPool = -1 }},
		{"negative cpu pool", func(c *Config) { c.RateLimit.CPUPool = -1 }},
		{"negative query timeout", func(c *Config) { c.Cost.QueryTimeout = -time.Second }},
		{"negative cache timeout", func(c *Config) { c.Cache.TTL = -time.Second }},
		{"tls cert without key", func(c *Config) { c.Server.Auth.TLS.Cert = "cert.pem" }},
		{"tls key without cert", func(c *Config) { c.Server.Auth.TLS.Key = "key.pem" }},
		{"proxy headers without trust boundary", func(c *Config) { c.Server.Auth.TrustProxyHeaders = true }},
		{"malformed trusted proxy CIDR", func(c *Config) {
			c.Server.Auth.TrustProxyHeaders = true
			c.Server.Auth.TrustedProxyCIDRs = []string{"not-a-cidr"}
		}},
		{"duplicate fields", func(c *Config) {
			c.Entities[0].Fields = []FieldConfig{{Name: "id"}, {Name: "id"}}
		}},
		{"duplicate params", func(c *Config) { c.Entities[0].Params = []string{"id", "id"} }},
		{"field ACL references excluded field", func(c *Config) {
			c.Entities[0].Fields = []FieldConfig{{Name: "id"}, {Name: "secret", Exclude: true}}
			c.Entities[0].FieldACL = map[string]FieldACLConfig{"reader": {Read: []string{"secret"}}}
		}},
		{"invalid row operator", func(c *Config) {
			c.Entities[0].RowPolicies = RowPolicies{
				"reader": {"and": []any{map[string]any{"op": "drop", "field": "id"}}},
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := valid()
			tc.mutate(cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestValidateRejectsBareTemplatesWithMultipleDatasources(t *testing.T) {
	t.Parallel()
	base := func() *Config {
		return &Config{Databases: map[string]DatabaseConfig{
			"primary": {Driver: "postgres", DSN: "x"},
			"replica": {Driver: "postgres", DSN: "y"},
		}}
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"bare allow", func(c *Config) { c.Cost.AllowTemplates = []string{"SELECT 1"} }, "allowTemplates"},
		{"bare reject", func(c *Config) { c.Cost.RejectTemplates = []string{"SELECT bad"} }, "rejectTemplates"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			tc.mutate(cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) ||
				!strings.Contains(err.Error(), "legacy bare SQL") ||
				!strings.Contains(err.Error(), "fp:v2:<sha256>") {
				t.Fatalf("Validate() error = %v, want clear %s migration error", err, tc.want)
			}
		})
	}

	cfg := base()
	cfg.Cost.AllowTemplates = []string{"fp:v2:abc", "primary:SELECT 1"}
	cfg.Cost.RejectTemplates = []string{"replica:SELECT bad"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("scoped multi-datasource templates error = %v", err)
	}

	single := &Config{
		Database: DatabaseConfig{Driver: "postgres", DSN: "x"},
		Cost: CostConfig{
			AllowTemplates:  []string{"SELECT 1"},
			RejectTemplates: []string{"SELECT bad"},
		},
	}
	if err := single.Validate(); err != nil {
		t.Fatalf("single-datasource compatibility error = %v", err)
	}
}

func TestSchemaIsValidJSON(t *testing.T) {
	t.Parallel()
	var schema map[string]any
	if err := json.Unmarshal(Schema(), &schema); err != nil {
		t.Fatalf("invalid schema: %v", err)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema properties are missing")
	}
	for _, name := range []string{"server", "database", "databases", "entities", "tools", "cost", "budget", "cache", "rateLimit", "mask", "audit", "transactions"} {
		if _, ok := properties[name]; !ok {
			t.Errorf("schema property %q is missing", name)
		}
	}
}

func TestYAMLCamelCaseConfigurationKeys(t *testing.T) {
	t.Parallel()
	var cfg Config
	if err := yaml.Unmarshal([]byte(`
databases:
  primary: {driver: postgres, dsn: "${DATABASE_DSN}"}
tools:
  describeEntities: true
cost:
  softScore: 70
  hardScore: 50
  maxRows: 500
budget:
  roles:
    reader:
      maxConcurrent: 2
rateLimit:
  maxInflight: 8
entities:
  - name: users
    datasource: primary
    primaryKey: [id]
    fieldACL:
      reader: {read: [id]}
`), &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.Tools.DescribeEntities || cfg.Cost.SoftScore != 70 || cfg.Cost.HardScore != 50 || cfg.Cost.MaxRows != 500 {
		t.Fatalf("camel-case keys were not decoded: %+v", cfg)
	}
	if cfg.Budget.Roles["reader"].MaxConcurrent != 2 || cfg.RateLimit.MaxInflight != 8 {
		t.Fatalf("nested camel-case keys were not decoded: %+v", cfg)
	}
	if len(cfg.Entities) != 1 || cfg.Entities[0].PrimaryKey[0] != "id" || len(cfg.Entities[0].FieldACL["reader"].Read) != 1 {
		t.Fatalf("entity camel-case keys were not decoded: %+v", cfg.Entities)
	}
}

func TestMaskEnabledOrDefault(t *testing.T) {
	t.Parallel()
	var nilMask MaskConfig
	if !nilMask.EnabledOrDefault() {
		t.Error("nil Enabled should default true")
	}
	f := false
	off := MaskConfig{Enabled: &f}
	if off.EnabledOrDefault() {
		t.Error("explicit false should be false")
	}
	tr := true
	on := MaskConfig{Enabled: &tr}
	if !on.EnabledOrDefault() {
		t.Error("explicit true should be true")
	}
}

func TestRateLimitEnabledOrDefault(t *testing.T) {
	t.Parallel()
	var nilRL RateLimitConfig
	if !nilRL.EnabledOrDefault() {
		t.Error("nil Enabled should default true")
	}
	f := false
	off := RateLimitConfig{Enabled: &f}
	if off.EnabledOrDefault() {
		t.Error("explicit false should be false")
	}
}

func TestRequirePKForWriteOrDefault(t *testing.T) {
	t.Parallel()
	var c CostConfig
	if !c.RequirePKForWriteOrDefault() {
		t.Error("nil requirePKForWrite must default true (safe)")
	}
	f := false
	c.RequirePKForWrite = &f
	if c.RequirePKForWriteOrDefault() {
		t.Error("explicit false should disable the PK write requirement")
	}
}

func TestValidateNamedDatabasesAndRelationships(t *testing.T) {
	cfg := &Config{
		Databases: map[string]DatabaseConfig{
			"primary": {Driver: "postgres", DSN: "x"},
			"archive": {Driver: "mysql", DSN: "y"},
		},
		Entities: []EntityConfig{
			{Name: "users", DataSource: "primary", Fields: []FieldConfig{{Name: "id"}}, Relationships: []RelationshipConfig{
				{Name: "orders", Target: "orders", Cardinality: "many", JoinOn: map[string]string{"id": "user_id"}},
			}},
			{Name: "orders", DataSource: "primary", Fields: []FieldConfig{{Name: "user_id"}}},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.Entities[1].DataSource = "archive"
	if err := cfg.Validate(); err == nil {
		t.Fatal("cross-datasource relationship must be rejected")
	}
}

func TestValidateRelationshipShape(t *testing.T) {
	base := Config{
		Database: DatabaseConfig{Driver: "postgres", DSN: "x"},
		Entities: []EntityConfig{
			{Name: "parents", Fields: []FieldConfig{{Name: "id"}}, Relationships: []RelationshipConfig{{
				Name: "children", Target: "children", Cardinality: "many",
				JoinOn: map[string]string{"id": "parent_id"},
			}}},
			{Name: "children", Fields: []FieldConfig{{Name: "parent_id"}}},
		},
	}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := []func(*Config){
		func(c *Config) { c.Entities[0].Relationships[0].Cardinality = "sometimes" },
		func(c *Config) { c.Entities[0].Relationships[0].JoinOn["other"] = "parent_id" },
		func(c *Config) { c.Entities[0].Relationships[0].JoinOn = map[string]string{"missing": "parent_id"} },
		func(c *Config) { c.Entities[0].Relationships[0].JoinOn = map[string]string{"id": "missing"} },
		func(c *Config) {
			c.Entities[0].Relationships = append(c.Entities[0].Relationships, c.Entities[0].Relationships[0])
		},
	}
	for i, mutate := range cases {
		cfg := base
		cfg.Entities = append([]EntityConfig(nil), base.Entities...)
		cfg.Entities[0].Relationships = append([]RelationshipConfig(nil), base.Entities[0].Relationships...)
		cfg.Entities[0].Relationships[0].JoinOn = map[string]string{"id": "parent_id"}
		mutate(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Fatalf("case %d: expected validation error", i)
		}
	}
}

func TestAQEExplainAnalyzeDefaultsAndValidation(t *testing.T) {
	var cfg Config
	if err := yaml.Unmarshal([]byte("database:\n  driver: postgres\n  dsn: x\ncost:\n  aqe:\n    explainAnalyze: true\n    sampleRate: 0.25\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.ApplyDefaults()
	if !cfg.Cost.AQE.ReadOnly || cfg.Cost.AQE.Timeout != time.Second {
		t.Fatalf("AQE safe defaults = %+v", cfg.Cost.AQE)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	cases := []string{
		"readOnly: false\n    sampleRate: 0.5\n    timeout: 1s",
		"readOnly: true\n    sampleRate: -0.1\n    timeout: 1s",
		"readOnly: true\n    sampleRate: 1.1\n    timeout: 1s",
		"readOnly: true\n    sampleRate: .nan\n    timeout: 1s",
		"readOnly: true\n    sampleRate: 0.5\n    timeout: 6s",
		"readOnly: true\n    sampleRate: 0.5\n    timeout: 0s",
	}
	for _, fields := range cases {
		var invalid Config
		raw := "database:\n  driver: postgres\n  dsn: x\ncost:\n  aqe:\n    explainAnalyze: true\n    " + fields + "\n"
		if err := yaml.Unmarshal([]byte(raw), &invalid); err != nil {
			t.Fatal(err)
		}
		invalid.ApplyDefaults()
		if err := invalid.Validate(); err == nil {
			t.Fatalf("expected invalid AQE config:\n%s", raw)
		}
	}
}

func TestAQEJSONTracksExplicitReadOnlyFalse(t *testing.T) {
	var cfg Config
	if err := json.Unmarshal([]byte(`{"database":{"driver":"postgres","dsn":"x"},"cost":{"aqe":{"explainAnalyze":true,"readOnly":false,"sampleRate":1,"timeout":1000000000}}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.ApplyDefaults()
	if cfg.Cost.AQE.ReadOnly {
		t.Fatal("explicit JSON readOnly false was overwritten")
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("explicit readOnly false must be rejected")
	}
}

func TestAQEExplainAnalyzeRejectsUnsupportedDatasource(t *testing.T) {
	cfg := Config{
		Databases: map[string]DatabaseConfig{
			"primary": {Driver: "postgres", DSN: "x"},
			"legacy":  {Driver: "mysql", DSN: "y"},
		},
		Cost: CostConfig{AQE: AQEConfig{
			ExplainAnalyze: true, ReadOnly: true, SampleRate: 0.1, Timeout: time.Second,
		}},
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err == nil {
		t.Fatal("MySQL datasource must fail fast when EXPLAIN ANALYZE is enabled")
	}
}

func TestApplyDefaultsMigratesLegacyDatabase(t *testing.T) {
	cfg := &Config{Database: DatabaseConfig{Driver: "postgres", DSN: "x"}}
	cfg.ApplyDefaults()
	if cfg.Databases["default"].Driver != "postgres" {
		t.Fatalf("databases = %+v", cfg.Databases)
	}
}
