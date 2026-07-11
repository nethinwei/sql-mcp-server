package config

import (
	"encoding/json"
	"errors"
	"time"
)

// Sentinel validation errors.
var (
	// ErrInvalidDriver is returned when Database.Driver is empty or malformed.
	ErrInvalidDriver = errors.New("config: invalid database driver")
	// ErrEmptyDSN is returned when Database.DSN is empty.
	ErrEmptyDSN = errors.New("config: empty database DSN")
	// ErrEmptyEntityName is returned when an entity has an empty name.
	ErrEmptyEntityName = errors.New("config: empty entity name")
)

// Config is the top-level configuration. Version is the configuration contract
// marker; the current value is "1" and loaders currently preserve it without
// rejecting other values.
type Config struct {
	Version      string                    `yaml:"version"      json:"version"`
	Server       ServerConfig              `yaml:"server"       json:"server"`
	Database     DatabaseConfig            `yaml:"database"     json:"database"`
	Databases    map[string]DatabaseConfig `yaml:"databases"    json:"databases"`
	Entities     []EntityConfig            `yaml:"entities"     json:"entities"`
	Tools        ToolFlags                 `yaml:"tools"        json:"tools"`
	Cost         CostConfig                `yaml:"cost"         json:"cost"`
	Budget       BudgetConfig              `yaml:"budget"       json:"budget"`
	Cache        CacheConfig               `yaml:"cache"        json:"cache"`
	RateLimit    RateLimitConfig           `yaml:"rateLimit"    json:"rateLimit"`
	Mask         MaskConfig                `yaml:"mask"         json:"mask"`
	Audit        AuditConfig               `yaml:"audit"        json:"audit"`
	Transactions TransactionConfig         `yaml:"transactions" json:"transactions"`
}

// Presence records fields whose explicit presence affects defaulting. Loaders
// for any encoding can populate it after decoding and call ApplyPresence.
type Presence struct {
	Tools          bool
	Cost           map[string]bool
	CostAQE        map[string]bool
	EntityDMLTools []bool
}

// ApplyPresence applies encoding-specific field presence to a decoded Config.
func (c *Config) ApplyPresence(p Presence) {
	c.Tools.present = p.Tools
	c.Cost.present = copyPresence(p.Cost)
	c.Cost.AQE.present = copyPresence(p.CostAQE)
	for i := range c.Entities {
		if i < len(p.EntityDMLTools) {
			c.Entities[i].MCP.dmlToolsSet = p.EntityDMLTools[i]
		}
	}
}

func copyPresence(src map[string]bool) map[string]bool {
	if src == nil {
		return nil
	}
	dst := make(map[string]bool, len(src))
	for key, present := range src {
		dst[key] = present
	}
	return dst
}

// ServerConfig holds transport and role settings.
type ServerConfig struct {
	Transport string        `yaml:"transport" json:"transport"` // "stdio" | "http"
	Addr      string        `yaml:"addr"      json:"addr"`      // http listen address
	Role      string        `yaml:"role"      json:"role"`      // runtime role (may be overridden by --role flag)
	Auth      AuthConfig    `yaml:"auth"      json:"auth"`      // http transport authentication
	Secrets   SecretsConfig `yaml:"secrets"   json:"secrets"`
}

// SecretsConfig restricts ${file:...} expansion to explicitly trusted roots.
type SecretsConfig struct {
	AllowedRoots []string `yaml:"allowedRoots" json:"allowedRoots"`
}

// AuthConfig configures HTTP transport authentication. When the listener is not
// bound to loopback, at least one of Token or TLS.ClientCA (mTLS) must be
// configured or startup is refused (fail-closed). The caller identity headers
// (X-MCP-Role / X-MCP-Subject) are trusted only when TrustProxyHeaders is true,
// and either mTLS or TrustedProxyCIDRs establishes the proxy trust boundary.
type AuthConfig struct {
	Token             string    `yaml:"token"             json:"token"`
	TrustProxyHeaders bool      `yaml:"trustProxyHeaders" json:"trustProxyHeaders"`
	TrustedProxyCIDRs []string  `yaml:"trustedProxyCIDRs" json:"trustedProxyCIDRs"`
	TLS               TLSConfig `yaml:"tls"               json:"tls"`
}

// TLSConfig configures TLS/mTLS for the HTTP transport. Cert+Key enable TLS;
// setting ClientCA additionally requires and verifies a client certificate.
type TLSConfig struct {
	Cert     string `yaml:"cert"     json:"cert"`
	Key      string `yaml:"key"      json:"key"`
	ClientCA string `yaml:"clientCA" json:"clientCA"`
}

// DatabaseConfig holds the connection target. DSN may contain ${ENV} or
// ${file:/path} placeholders resolved by x/bootstrap.
type DatabaseConfig struct {
	Driver string `yaml:"driver" json:"driver"`
	DSN    string `yaml:"dsn"    json:"dsn"`
}

// FilterConfig is a declarative filter (JSON object) for row-level policies,
// converted to a relalg.Predicate by x/bootstrap.
type FilterConfig = map[string]any

// RowPolicies maps a role name to its row-level filter.
type RowPolicies map[string]FilterConfig

// EntityConfig is the configuration view of one entity.
type EntityConfig struct {
	Name          string                    `yaml:"name"          json:"name"`
	Source        string                    `yaml:"source"        json:"source"`
	DataSource    string                    `yaml:"datasource"    json:"datasource"`
	Schema        string                    `yaml:"schema"        json:"schema"`
	Kind          string                    `yaml:"kind"          json:"kind"` // table | view | procedure
	Description   string                    `yaml:"description"   json:"description"`
	PrimaryKey    []string                  `yaml:"primaryKey"    json:"primaryKey"`
	Fields        []FieldConfig             `yaml:"fields"        json:"fields"`
	Roles         RoleConfig                `yaml:"roles"         json:"roles"`
	FieldACL      map[string]FieldACLConfig `yaml:"fieldACL"      json:"fieldACL"`
	MCP           MCPFlags                  `yaml:"mcp"           json:"mcp"`
	RowPolicies   RowPolicies               `yaml:"rowPolicies"   json:"rowPolicies"`
	Relationships []RelationshipConfig      `yaml:"relationships" json:"relationships"`
	// Params is the ordered formal-parameter list for a procedure entity, bound
	// positionally by execute_entity. Required for procedures.
	Params []string `yaml:"params"        json:"params"`
}

// RelationshipConfig configures a same-data-source batch expansion.
type RelationshipConfig struct {
	Name        string            `yaml:"name"        json:"name"`
	Target      string            `yaml:"target"      json:"target"`
	Cardinality string            `yaml:"cardinality" json:"cardinality"`
	JoinOn      map[string]string `yaml:"joinOn"      json:"joinOn"`
}

// FieldConfig configures one column.
type FieldConfig struct {
	Name        string `yaml:"name"        json:"name"`
	Alias       string `yaml:"alias"       json:"alias"`
	Description string `yaml:"description" json:"description"`
	Mask        string `yaml:"mask"        json:"mask"` // mask rule name (see mask package)
	Exclude     bool   `yaml:"exclude"     json:"exclude"`
}

// RoleConfig lists allowed roles per action.
type RoleConfig struct {
	Read      []string `yaml:"read"      json:"read"`
	Create    []string `yaml:"create"    json:"create"`
	Update    []string `yaml:"update"    json:"update"`
	Delete    []string `yaml:"delete"    json:"delete"`
	Execute   []string `yaml:"execute"   json:"execute"`
	Aggregate []string `yaml:"aggregate" json:"aggregate"`
}

// FieldACLConfig restricts one role to named readable and writable fields.
// Omitting a role leaves existing entity-level authorization behavior intact.
type FieldACLConfig struct {
	Read  []string `yaml:"read"  json:"read"`
	Write []string `yaml:"write" json:"write"`
}

// MCPFlags controls entity participation in MCP. Zero value means "not set";
// ApplyDefaults sets DMLTools=true for unset entities.
type MCPFlags struct {
	DMLTools         bool `yaml:"dmlTools"         json:"dmlTools"`
	CustomTool       bool `yaml:"customTool"       json:"customTool"`
	TrustedProcedure bool `yaml:"trustedProcedure" json:"trustedProcedure"`
	dmlToolsSet      bool
}

// MCPFlagsWithDMLTools constructs MCP flags with an explicit dmlTools value.
// Use it for programmatic configuration when false must differ from omission.
func MCPFlagsWithDMLTools(enabled bool) MCPFlags {
	return MCPFlags{DMLTools: enabled, dmlToolsSet: true}
}

// UnmarshalJSON preserves an explicit false dmlTools value.
func (f *MCPFlags) UnmarshalJSON(data []byte) error {
	type plain MCPFlags
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*f = MCPFlags(decoded)
	_, f.dmlToolsSet = raw["dmlTools"]
	return nil
}

// ToolFlags toggles each DML tool. Decoding tracks whether the tools node was
// present so an explicit all-false object differs from omission.
type ToolFlags struct {
	DescribeEntities    bool `yaml:"describeEntities"    json:"describeEntities"`
	ReadRecords         bool `yaml:"readRecords"         json:"readRecords"`
	CreateRecord        bool `yaml:"createRecord"        json:"createRecord"`
	UpdateRecord        bool `yaml:"updateRecord"        json:"updateRecord"`
	DeleteRecord        bool `yaml:"deleteRecord"        json:"deleteRecord"`
	ExecuteEntity       bool `yaml:"executeEntity"       json:"executeEntity"`
	AggregateRecords    bool `yaml:"aggregateRecords"    json:"aggregateRecords"`
	BeginTransaction    bool `yaml:"beginTransaction"    json:"beginTransaction"`
	CommitTransaction   bool `yaml:"commitTransaction"   json:"commitTransaction"`
	RollbackTransaction bool `yaml:"rollbackTransaction" json:"rollbackTransaction"`
	present             bool
}

// ExplicitToolFlags marks a programmatically constructed tools block present,
// including the meaningful all-false configuration.
func ExplicitToolFlags(flags ToolFlags) ToolFlags {
	flags.present = true
	return flags
}

// UnmarshalJSON records that the tools object was present.
func (f *ToolFlags) UnmarshalJSON(data []byte) error {
	type plain ToolFlags
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*f = ToolFlags(decoded)
	f.present = true
	return nil
}

// TransactionConfig bounds explicit transaction lifetime and cardinality.
type TransactionConfig struct {
	TTL             time.Duration `yaml:"ttl"             json:"ttl"`
	MaxOpen         int           `yaml:"maxOpen"         json:"maxOpen"`
	BeginTimeout    time.Duration `yaml:"beginTimeout"    json:"beginTimeout"`
	CommitTimeout   time.Duration `yaml:"commitTimeout"   json:"commitTimeout"`
	RollbackTimeout time.Duration `yaml:"rollbackTimeout" json:"rollbackTimeout"`
}

// CostConfig configures the defense-in-depth cost gate. SoftScore/HardScore are
// the 0-100 normalized thresholds (from cost.ScorePlan) for soft/hard reject.
type CostConfig struct {
	Enabled             *bool         `yaml:"enabled"             json:"enabled"`
	SoftScore           int           `yaml:"softScore"           json:"softScore"`
	HardScore           int           `yaml:"hardScore"           json:"hardScore"`
	MaxRows             int64         `yaml:"maxRows"             json:"maxRows"`
	MaxBytes            int64         `yaml:"maxBytes"            json:"maxBytes"`
	MaxINListSize       int           `yaml:"maxINListSize"       json:"maxINListSize"`
	MaxFilterConditions int           `yaml:"maxFilterConditions" json:"maxFilterConditions"`
	MaxGroupByFields    int           `yaml:"maxGroupByFields"    json:"maxGroupByFields"`
	MaxAggregates       int           `yaml:"maxAggregates"       json:"maxAggregates"`
	MaxExpand           int           `yaml:"maxExpand"           json:"maxExpand"`
	MaxProcedureRows    int64         `yaml:"maxProcedureRows"    json:"maxProcedureRows"`
	RejectFullScan      bool          `yaml:"rejectFullScan"      json:"rejectFullScan"`
	WhitelistPKPoint    bool          `yaml:"whitelistPKPoint"    json:"whitelistPKPoint"`
	RequirePKForWrite   *bool         `yaml:"requirePKForWrite"   json:"requirePKForWrite"`
	RequireKnownScan    bool          `yaml:"requireKnownScan"    json:"requireKnownScan"`
	RequireFreshStats   bool          `yaml:"requireFreshStats"   json:"requireFreshStats"`
	QueryTimeout        time.Duration `yaml:"queryTimeout"        json:"queryTimeout"`
	AllowTemplates      []string      `yaml:"allowTemplates"      json:"allowTemplates"`
	RejectTemplates     []string      `yaml:"rejectTemplates"     json:"rejectTemplates"`
	AQE                 AQEConfig     `yaml:"aqe"                 json:"aqe"`
	present             map[string]bool
}

// UnmarshalJSON tracks each cost field independently.
func (c *CostConfig) UnmarshalJSON(data []byte) error {
	type plain CostConfig
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*c = CostConfig(decoded)
	c.present = make(map[string]bool, len(raw))
	for key := range raw {
		c.present[key] = true
	}
	return nil
}

// AQEConfig controls bounded estimate feedback and optional read-only
// EXPLAIN ANALYZE sampling.
type AQEConfig struct {
	WindowSize        int           `yaml:"windowSize"        json:"windowSize"`
	AnomalyFactor     float64       `yaml:"anomalyFactor"     json:"anomalyFactor"`
	AnomalyMinSamples int           `yaml:"anomalyMinSamples" json:"anomalyMinSamples"`
	ExplainAnalyze    bool          `yaml:"explainAnalyze"    json:"explainAnalyze"`
	ReadOnly          bool          `yaml:"readOnly"          json:"readOnly"`
	SampleRate        float64       `yaml:"sampleRate"        json:"sampleRate"`
	Timeout           time.Duration `yaml:"timeout"           json:"timeout"`
	MaxFingerprints   int           `yaml:"maxFingerprints"   json:"maxFingerprints"`
	present           map[string]bool
}

// UnmarshalJSON tracks optional safety fields.
func (c *AQEConfig) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	type plain AQEConfig
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*c = AQEConfig(decoded)
	c.present = make(map[string]bool, len(raw))
	for key := range raw {
		c.present[key] = true
	}
	return nil
}

// BudgetConfig contains role and tenant scoped resource limits.
type BudgetConfig struct {
	Roles   map[string]BudgetLimits `yaml:"roles"   json:"roles"`
	Tenants map[string]BudgetLimits `yaml:"tenants" json:"tenants"`
}

// BudgetLimits is unlimited when all fields are zero.
type BudgetLimits struct {
	MaxConcurrent           int           `yaml:"maxConcurrent"            json:"maxConcurrent"`
	MaxExecution            time.Duration `yaml:"maxExecution"             json:"maxExecution"`
	MaxEstimatedScannedRows int64         `yaml:"maxEstimatedScannedRows"  json:"maxEstimatedScannedRows"`
	MaxScannedRows          int64         `yaml:"maxScannedRows,omitempty" json:"maxScannedRows,omitempty"` // deprecated
	MaxReturnedRows         int64         `yaml:"maxReturnedRows"          json:"maxReturnedRows"`
	MaxReturnedBytes        int64         `yaml:"maxReturnedBytes"         json:"maxReturnedBytes"`
	MaxSessionCost          int64         `yaml:"maxSessionCost"           json:"maxSessionCost"`
}

// EnabledOrDefault reports whether cost protection is on; nil means default
// true. A pointer preserves an explicit enabled: false through YAML decoding.
func (c CostConfig) EnabledOrDefault() bool {
	if c.Enabled != nil {
		return *c.Enabled
	}
	return true
}

// Bool returns a pointer to v for programmatically constructed tri-state
// configuration fields.
func Bool(v bool) *bool {
	return &v
}

// RequirePKForWriteOrDefault reports whether writes must be primary-key scoped.
// A nil setting defaults to true (safe): a non-point UPDATE/DELETE is rejected
// unless explicitly allowed via allowTemplates.
func (c CostConfig) RequirePKForWriteOrDefault() bool {
	if c.RequirePKForWrite != nil {
		return *c.RequirePKForWrite
	}
	return true
}

// CacheConfig configures the read cache.
type CacheConfig struct {
	Enabled         bool          `yaml:"enabled"         json:"enabled"`
	TTL             time.Duration `yaml:"ttl"             json:"ttl"`
	MaxSize         int           `yaml:"maxSize"         json:"maxSize"`
	MaxEntryRows    int           `yaml:"maxEntryRows"    json:"maxEntryRows"`
	MaxEntryBytes   int64         `yaml:"maxEntryBytes"   json:"maxEntryBytes"`
	PreparedMaxSize int           `yaml:"preparedMaxSize" json:"preparedMaxSize"`
}

// RateLimitConfig configures the engine's concurrency and rate limits.
type RateLimitConfig struct {
	Enabled          *bool         `yaml:"enabled"          json:"enabled"`
	RPS              float64       `yaml:"rps"              json:"rps"`
	MaxInflight      int           `yaml:"maxInflight"      json:"maxInflight"`
	IOPool           int           `yaml:"ioPool"           json:"ioPool"`
	CPUPool          int           `yaml:"cpuPool"          json:"cpuPool"`
	MinConcurrency   int           `yaml:"minConcurrency"   json:"minConcurrency"`
	RTTThreshold     time.Duration `yaml:"rttThreshold"     json:"rttThreshold"`
	BreakerThreshold int           `yaml:"breakerThreshold" json:"breakerThreshold"`
	BreakerCooldown  time.Duration `yaml:"breakerCooldown"  json:"breakerCooldown"`
	ConnMaxIdleTime  time.Duration `yaml:"connMaxIdleTime"  json:"connMaxIdleTime"`
	ConnMaxLifetime  time.Duration `yaml:"connMaxLifetime"  json:"connMaxLifetime"`
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
	Enabled   bool   `yaml:"enabled"   json:"enabled"`
	Path      string `yaml:"path"      json:"path"`
	QueueSize int    `yaml:"queueSize" json:"queueSize"`
}

// DefaultToolFlags returns the safe default tool set: all tools enabled except
// delete_record, which is off by default to prevent accidental data loss.
func DefaultToolFlags() ToolFlags {
	return ToolFlags{
		DescribeEntities:    true,
		ReadRecords:         true,
		CreateRecord:        true,
		UpdateRecord:        true,
		DeleteRecord:        false,
		ExecuteEntity:       true,
		AggregateRecords:    true,
		BeginTransaction:    true,
		CommitTransaction:   true,
		RollbackTransaction: true,
	}
}
