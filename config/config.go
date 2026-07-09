package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"time"
)

// Sentinel validation errors.
var (
	// ErrInvalidDriver is returned when Database.Driver is not a supported name.
	ErrInvalidDriver = errors.New("config: invalid database driver")
	// ErrEmptyDSN is returned when Database.DSN is empty.
	ErrEmptyDSN = errors.New("config: empty database DSN")
	// ErrEmptyEntityName is returned when an entity has an empty name.
	ErrEmptyEntityName = errors.New("config: empty entity name")
)

// Config is the top-level configuration. Version is bumped on incompatible
// changes and validated at load time.
type Config struct {
	Version   string
	Server    ServerConfig
	Database  DatabaseConfig
	Entities  []EntityConfig
	Tools     ToolFlags
	Cost      CostConfig
	Cache     CacheConfig
	RateLimit RateLimitConfig
	Mask      MaskConfig
	Audit     AuditConfig
}

// ServerConfig holds transport and role settings.
type ServerConfig struct {
	Transport string // "stdio" | "http"
	Addr      string // http listen address
	Role      string // runtime role (may be overridden by --role flag)
}

// DatabaseConfig holds the connection target. DSN may contain ${ENV} or
// ${file:/path} placeholders resolved by x/bootstrap.
type DatabaseConfig struct {
	Driver string // postgres | mysql | oceanbase
	DSN    string
}

// FilterConfig is a declarative filter (JSON object) for row-level policies,
// converted to a relalg.Predicate by x/bootstrap.
type FilterConfig = map[string]any

// RowPolicies maps a role name to its row-level filter.
type RowPolicies map[string]FilterConfig

// EntityConfig is the configuration view of one entity.
type EntityConfig struct {
	Name        string
	Source      string
	Schema      string
	Kind        string // table | view | procedure
	Description string
	PrimaryKey  []string
	Fields      []FieldConfig
	Roles       RoleConfig
	MCP         MCPFlags
	RowPolicies RowPolicies
}

// FieldConfig configures one column.
type FieldConfig struct {
	Name        string
	Alias       string
	Description string
	Mask        string // mask rule name (see mask package)
	Exclude     bool
}

// RoleConfig lists allowed roles per action.
type RoleConfig struct {
	Read      []string
	Create    []string
	Update    []string
	Delete    []string
	Execute   []string
	Aggregate []string
}

// MCPFlags controls entity participation in MCP. Zero value means "not set";
// ApplyDefaults sets DMLTools=true for unset entities.
type MCPFlags struct {
	DMLTools   bool
	CustomTool bool
}

// ToolFlags toggles each DML tool. Zero value means "tools section omitted";
// ApplyDefaults applies DefaultToolFlags in that case.
type ToolFlags struct {
	DescribeEntities bool
	ReadRecords      bool
	CreateRecord     bool
	UpdateRecord     bool
	DeleteRecord     bool
	ExecuteEntity    bool
	AggregateRecords bool
}

// CostConfig configures the defense-in-depth cost gate. SoftScore/HardScore are
// the 0-100 normalized thresholds (from cost.ScorePlan) for soft/hard reject.
type CostConfig struct {
	Enabled           bool
	SoftScore         int
	HardScore         int
	MaxRows           int64
	MaxBytes          int64
	RejectFullScan    bool
	WhitelistPKPoint  bool
	RequireKnownScan  bool
	RequireFreshStats bool
	QueryTimeout      time.Duration
	AllowTemplates    []string `yaml:"allowTemplates" json:"allowTemplates"`
	RejectTemplates   []string `yaml:"rejectTemplates" json:"rejectTemplates"`
}

// CacheConfig configures the read cache.
type CacheConfig struct {
	Enabled bool
	TTL     time.Duration
	MaxSize int
}

// RateLimitConfig configures the engine's concurrency and rate limits.
type RateLimitConfig struct {
	Enabled          *bool `yaml:"enabled" json:"enabled"`
	RPS              float64
	MaxInflight      int
	IOPool           int
	CPUPool          int
	MinConcurrency   int           `yaml:"minConcurrency" json:"minConcurrency"`
	RTTThreshold     time.Duration `yaml:"rttThreshold" json:"rttThreshold"`
	BreakerThreshold int           `yaml:"breakerThreshold" json:"breakerThreshold"`
	BreakerCooldown  time.Duration `yaml:"breakerCooldown" json:"breakerCooldown"`
	ConnMaxIdleTime  time.Duration `yaml:"connMaxIdleTime" json:"connMaxIdleTime"`
}

// EnabledOrDefault reports whether rate limiting is on; nil means default true.
func (c RateLimitConfig) EnabledOrDefault() bool {
	if c.Enabled != nil {
		return *c.Enabled
	}
	return true
}

// MaskConfig controls field masking. Enabled defaults to true when unset.
type MaskConfig struct {
	Enabled *bool `yaml:"enabled" json:"enabled"`
}

// EnabledOrDefault reports whether masking is on; nil means default true.
func (c MaskConfig) EnabledOrDefault() bool {
	if c.Enabled != nil {
		return *c.Enabled
	}
	return true
}

// AuditConfig configures the audit sink.
type AuditConfig struct {
	Enabled   bool
	Path      string
	QueueSize int `yaml:"queueSize" json:"queueSize"`
}

// DefaultToolFlags returns the safe default tool set: all tools enabled except
// delete_record, which is off by default to prevent accidental data loss.
func DefaultToolFlags() ToolFlags {
	return ToolFlags{
		DescribeEntities: true,
		ReadRecords:      true,
		CreateRecord:     true,
		UpdateRecord:     true,
		DeleteRecord:     false,
		ExecuteEntity:    true,
		AggregateRecords: true,
	}
}

// ApplyDefaults fills unset fields with safe defaults. A zero Tools section
// receives DefaultToolFlags; entities with unset MCP get DMLTools=true; cost,
// when enabled with zero thresholds, gets sane limits; rate limits default to
// conservative bounds.
func (c *Config) ApplyDefaults() {
	if c.Tools == (ToolFlags{}) {
		c.Tools = DefaultToolFlags()
	}
	for i := range c.Entities {
		if c.Entities[i].MCP == (MCPFlags{}) {
			c.Entities[i].MCP = MCPFlags{DMLTools: true}
		}
	}
	if c.Cost.Enabled && c.Cost.HardScore == 0 && c.Cost.MaxRows == 0 {
		c.Cost.SoftScore = 40
		c.Cost.HardScore = 70
		c.Cost.MaxRows = 10000
		c.Cost.WhitelistPKPoint = true
	}
	if c.RateLimit.MaxInflight == 0 {
		c.RateLimit.MaxInflight = 256
	}
	if c.RateLimit.IOPool == 0 {
		c.RateLimit.IOPool = 16
	}
	if c.RateLimit.CPUPool == 0 {
		c.RateLimit.CPUPool = runtime.NumCPU()
	}
	if c.RateLimit.MinConcurrency == 0 {
		c.RateLimit.MinConcurrency = 1
	}
	if c.RateLimit.BreakerThreshold == 0 {
		c.RateLimit.BreakerThreshold = 5
	}
	if c.RateLimit.BreakerCooldown == 0 {
		c.RateLimit.BreakerCooldown = 5 * time.Second
	}
	if c.RateLimit.ConnMaxIdleTime == 0 {
		c.RateLimit.ConnMaxIdleTime = 5 * time.Minute
	}
	if c.Audit.QueueSize == 0 {
		c.Audit.QueueSize = 1024
	}
	if c.Cache.Enabled && c.Cache.TTL == 0 {
		c.Cache.TTL = 30 * time.Second
	}
	if c.Server.Transport == "" {
		c.Server.Transport = "stdio"
	}
}

// Validate checks required fields. DSN placeholder resolution happens later in
// x/bootstrap; here we only require non-empty.
func (c *Config) Validate() error {
	switch c.Database.Driver {
	case "postgres", "mysql", "oceanbase":
	default:
		return fmt.Errorf("%w: %q", ErrInvalidDriver, c.Database.Driver)
	}
	if c.Database.DSN == "" {
		return ErrEmptyDSN
	}
	for _, e := range c.Entities {
		if e.Name == "" {
			return ErrEmptyEntityName
		}
	}
	return nil
}

// Schema returns a JSON Schema describing the configuration, for IDE validation
// and documentation. It covers the top-level shape; expand as the model grows.
func Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "required": ["database", "entities"],
  "properties": {
    "version": {"type": "string"},
    "server": {"type": "object", "properties": {"transport": {"type": "string"}, "addr": {"type": "string"}, "role": {"type": "string"}}},
    "database": {"type": "object", "required": ["driver", "dsn"], "properties": {"driver": {"type": "string"}, "dsn": {"type": "string"}}},
    "entities": {"type": "array"},
    "tools": {"type": "object"},
    "cost": {"type": "object"},
    "cache": {"type": "object"},
    "rateLimit": {"type": "object"},
    "audit": {"type": "object"}
  }
}`)
}
