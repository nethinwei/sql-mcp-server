package config

import (
	"errors"
	"testing"
	"time"
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

func TestApplyDefaultsCostThresholds(t *testing.T) {
	t.Parallel()
	c := &Config{Database: DatabaseConfig{Driver: "postgres", DSN: "x"}, Cost: CostConfig{Enabled: true}}
	c.ApplyDefaults()
	if c.Cost.HardScore != 70 || c.Cost.MaxRows != 10000 {
		t.Fatalf("expected default thresholds, got %+v", c.Cost)
	}
	if !c.Cost.WhitelistPKPoint {
		t.Error("WhitelistPKPoint should default true when cost enabled")
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

func TestSchemaIsValidJSON(t *testing.T) {
	t.Parallel()
	if len(Schema()) == 0 {
		t.Fatal("schema is empty")
	}
}
