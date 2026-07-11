package configyaml

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const validDatabase = "database: {driver: postgres, dsn: x}\n"

func TestDecodePreservesToolsAndEntityPresence(t *testing.T) {
	t.Parallel()

	omitted, err := Decode([]byte(validDatabase + "entities:\n  - name: visible\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !omitted.Tools.ReadRecords || !omitted.Entities[0].MCP.DMLTools {
		t.Fatalf("omitted values did not receive defaults: %+v", omitted)
	}

	explicit, err := Decode(
		[]byte(validDatabase + "tools: {}\nentities:\n  - name: hidden\n    mcp:\n      dmlTools: false\n"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Tools.ReadRecords || explicit.Tools.CreateRecord {
		t.Fatalf("explicit all-false tools changed: %+v", explicit.Tools)
	}
	if explicit.Entities[0].MCP.DMLTools {
		t.Fatal("explicit mcp.dmlTools false was overwritten")
	}
}

func TestDecodePreservesCostPresence(t *testing.T) {
	t.Parallel()

	cfg, err := Decode([]byte(validDatabase + `cost:
  enabled: true
  softScore: 0
  hardScore: 0
  maxRows: 50
  maxBytes: 100
  maxProcedureRows: 10
  whitelistPKPoint: false
  rejectFullScan: false
  requireKnownScan: false
  queryTimeout: 0s
  aqe:
    readOnly: false
    timeout: 0s
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Cost.SoftScore != 0 || cfg.Cost.HardScore != 0 ||
		cfg.Cost.WhitelistPKPoint || cfg.Cost.RejectFullScan || cfg.Cost.RequireKnownScan ||
		cfg.Cost.QueryTimeout != 0 {
		t.Fatalf("explicit cost zero/false values changed: %+v", cfg.Cost)
	}
	if cfg.Cost.AQE.ReadOnly || cfg.Cost.AQE.Timeout != 0 {
		t.Fatalf("explicit AQE zero/false values changed: %+v", cfg.Cost.AQE)
	}

	omitted, err := Decode([]byte(validDatabase + "cost: {softScore: 70}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if omitted.Cost.SoftScore != 70 || omitted.Cost.HardScore != 40 ||
		!omitted.Cost.WhitelistPKPoint || !omitted.Cost.RejectFullScan ||
		!omitted.Cost.RequireKnownScan || omitted.Cost.QueryTimeout != 30*time.Second ||
		!omitted.Cost.AQE.ReadOnly || omitted.Cost.AQE.Timeout != time.Second {
		t.Fatalf("omitted cost fields did not receive defaults: %+v", omitted.Cost)
	}
}

func TestDecodeRejectsExplicitZeroMandatoryCostLimit(t *testing.T) {
	t.Parallel()
	if _, err := Decode([]byte(validDatabase + "cost: {maxRows: 0}\n")); err == nil {
		t.Fatal("explicit zero maxRows must not be replaced by its default")
	}
}

func TestDecodeCostEnabledTriState(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		cost string
		want bool
	}{
		{"omitted", "{}", true},
		{"explicit false", "{enabled: false}", false},
		{"explicit true", "{enabled: true}", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Decode([]byte(validDatabase + "cost: " + tc.cost + "\n"))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Cost.EnabledOrDefault() != tc.want {
				t.Fatalf("Enabled = %v, want %v", cfg.Cost.EnabledOrDefault(), tc.want)
			}
		})
	}
}

func TestDecodeCamelCaseConfigurationKeys(t *testing.T) {
	t.Parallel()
	cfg, err := Decode([]byte(`
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
    fields: [{name: id}]
    fieldACL:
      reader: {read: [id]}
`))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Tools.DescribeEntities || cfg.Cost.SoftScore != 70 || cfg.Cost.HardScore != 50 || cfg.Cost.MaxRows != 500 {
		t.Fatalf("camel-case keys were not decoded: %+v", cfg)
	}
	if cfg.Budget.Roles["reader"].MaxConcurrent != 2 || cfg.RateLimit.MaxInflight != 8 {
		t.Fatalf("nested camel-case keys were not decoded: %+v", cfg)
	}
	if len(cfg.Entities) != 1 || cfg.Entities[0].PrimaryKey[0] != "id" ||
		len(cfg.Entities[0].FieldACL["reader"].Read) != 1 {
		t.Fatalf("entity camel-case keys were not decoded: %+v", cfg.Entities)
	}
}

func TestDecodeAQEDefaultsAndValidation(t *testing.T) {
	t.Parallel()
	cfg, err := Decode([]byte(validDatabase + "cost:\n  aqe:\n    explainAnalyze: true\n    sampleRate: 0.25\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Cost.AQE.ReadOnly || cfg.Cost.AQE.Timeout != time.Second {
		t.Fatalf("AQE safe defaults = %+v", cfg.Cost.AQE)
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
		raw := validDatabase + "cost:\n  aqe:\n    explainAnalyze: true\n    " + fields + "\n"
		if _, err := Decode([]byte(raw)); err == nil {
			t.Fatalf("expected invalid AQE config:\n%s", raw)
		}
	}
}

func TestDecodeDurationStrings(t *testing.T) {
	t.Parallel()
	cfg, err := Decode([]byte(validDatabase + `cost:
  queryTimeout: 2s
cache:
  enabled: true
  ttl: 3s
transactions:
  ttl: 4s
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Cost.QueryTimeout != 2*time.Second || cfg.Cache.TTL != 3*time.Second ||
		cfg.Transactions.TTL != 4*time.Second {
		t.Fatalf("duration strings decoded incorrectly: cost=%v cache=%v transaction=%v",
			cfg.Cost.QueryTimeout, cfg.Cache.TTL, cfg.Transactions.TTL)
	}
}

func TestLoad(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(validDatabase), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Databases["default"].Driver != "postgres" {
		t.Fatalf("loaded databases = %+v", cfg.Databases)
	}
}
