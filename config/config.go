package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"runtime"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/nethinwei/sql-mcp-server/cost"
	"github.com/nethinwei/sql-mcp-server/relalg"
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

// Config is the top-level configuration. Version is the configuration contract
// marker; the current value is "1" and loaders currently preserve it without
// rejecting other values.
type Config struct {
	Version      string                    `yaml:"version" json:"version"`
	Server       ServerConfig              `yaml:"server" json:"server"`
	Database     DatabaseConfig            `yaml:"database" json:"database"`
	Databases    map[string]DatabaseConfig `yaml:"databases" json:"databases"`
	Entities     []EntityConfig            `yaml:"entities" json:"entities"`
	Tools        ToolFlags                 `yaml:"tools" json:"tools"`
	Cost         CostConfig                `yaml:"cost" json:"cost"`
	Budget       BudgetConfig              `yaml:"budget" json:"budget"`
	Cache        CacheConfig               `yaml:"cache" json:"cache"`
	RateLimit    RateLimitConfig           `yaml:"rateLimit" json:"rateLimit"`
	Mask         MaskConfig                `yaml:"mask" json:"mask"`
	Audit        AuditConfig               `yaml:"audit" json:"audit"`
	Transactions TransactionConfig         `yaml:"transactions" json:"transactions"`
}

// ServerConfig holds transport and role settings.
type ServerConfig struct {
	Transport string     `yaml:"transport" json:"transport"` // "stdio" | "http"
	Addr      string     `yaml:"addr" json:"addr"`           // http listen address
	Role      string     `yaml:"role" json:"role"`           // runtime role (may be overridden by --role flag)
	Auth      AuthConfig `yaml:"auth" json:"auth"`           // http transport authentication
}

// AuthConfig configures HTTP transport authentication. When the listener is not
// bound to loopback, at least one of Token or TLS.ClientCA (mTLS) must be
// configured or startup is refused (fail-closed). The caller identity headers
// (X-MCP-Role / X-MCP-Subject) are trusted only when TrustProxyHeaders is true,
// and either mTLS or TrustedProxyCIDRs establishes the proxy trust boundary.
type AuthConfig struct {
	Token             string    `yaml:"token" json:"token"`
	TrustProxyHeaders bool      `yaml:"trustProxyHeaders" json:"trustProxyHeaders"`
	TrustedProxyCIDRs []string  `yaml:"trustedProxyCIDRs" json:"trustedProxyCIDRs"`
	TLS               TLSConfig `yaml:"tls" json:"tls"`
}

// TLSConfig configures TLS/mTLS for the HTTP transport. Cert+Key enable TLS;
// setting ClientCA additionally requires and verifies a client certificate.
type TLSConfig struct {
	Cert     string `yaml:"cert" json:"cert"`
	Key      string `yaml:"key" json:"key"`
	ClientCA string `yaml:"clientCA" json:"clientCA"`
}

// DatabaseConfig holds the connection target. DSN may contain ${ENV} or
// ${file:/path} placeholders resolved by x/bootstrap.
type DatabaseConfig struct {
	Driver string `yaml:"driver" json:"driver"` // postgres | mysql | oceanbase
	DSN    string `yaml:"dsn" json:"dsn"`
}

// FilterConfig is a declarative filter (JSON object) for row-level policies,
// converted to a relalg.Predicate by x/bootstrap.
type FilterConfig = map[string]any

// RowPolicies maps a role name to its row-level filter.
type RowPolicies map[string]FilterConfig

// EntityConfig is the configuration view of one entity.
type EntityConfig struct {
	Name          string                    `yaml:"name" json:"name"`
	Source        string                    `yaml:"source" json:"source"`
	DataSource    string                    `yaml:"datasource" json:"datasource"`
	Schema        string                    `yaml:"schema" json:"schema"`
	Kind          string                    `yaml:"kind" json:"kind"` // table | view | procedure
	Description   string                    `yaml:"description" json:"description"`
	PrimaryKey    []string                  `yaml:"primaryKey" json:"primaryKey"`
	Fields        []FieldConfig             `yaml:"fields" json:"fields"`
	Roles         RoleConfig                `yaml:"roles" json:"roles"`
	FieldACL      map[string]FieldACLConfig `yaml:"fieldACL" json:"fieldACL"`
	MCP           MCPFlags                  `yaml:"mcp" json:"mcp"`
	RowPolicies   RowPolicies               `yaml:"rowPolicies" json:"rowPolicies"`
	Relationships []RelationshipConfig      `yaml:"relationships" json:"relationships"`
	// Params is the ordered formal-parameter list for a procedure entity, bound
	// positionally by execute_entity. Required for procedures.
	Params []string `yaml:"params" json:"params"`
}

// RelationshipConfig configures a same-data-source batch expansion.
type RelationshipConfig struct {
	Name        string            `yaml:"name" json:"name"`
	Target      string            `yaml:"target" json:"target"`
	Cardinality string            `yaml:"cardinality" json:"cardinality"`
	JoinOn      map[string]string `yaml:"joinOn" json:"joinOn"`
}

// FieldConfig configures one column.
type FieldConfig struct {
	Name        string `yaml:"name" json:"name"`
	Alias       string `yaml:"alias" json:"alias"`
	Description string `yaml:"description" json:"description"`
	Mask        string `yaml:"mask" json:"mask"` // mask rule name (see mask package)
	Exclude     bool   `yaml:"exclude" json:"exclude"`
}

// RoleConfig lists allowed roles per action.
type RoleConfig struct {
	Read      []string `yaml:"read" json:"read"`
	Create    []string `yaml:"create" json:"create"`
	Update    []string `yaml:"update" json:"update"`
	Delete    []string `yaml:"delete" json:"delete"`
	Execute   []string `yaml:"execute" json:"execute"`
	Aggregate []string `yaml:"aggregate" json:"aggregate"`
}

// FieldACLConfig restricts one role to named readable and writable fields.
// Omitting a role leaves existing entity-level authorization behavior intact.
type FieldACLConfig struct {
	Read  []string `yaml:"read" json:"read"`
	Write []string `yaml:"write" json:"write"`
}

// MCPFlags controls entity participation in MCP. Zero value means "not set";
// ApplyDefaults sets DMLTools=true for unset entities.
type MCPFlags struct {
	DMLTools    bool `yaml:"dmlTools" json:"dmlTools"`
	CustomTool  bool `yaml:"customTool" json:"customTool"`
	dmlToolsSet bool
}

// MCPFlagsWithDMLTools constructs MCP flags with an explicit dmlTools value.
// Use it for programmatic configuration when false must differ from omission.
func MCPFlagsWithDMLTools(enabled bool) MCPFlags {
	return MCPFlags{DMLTools: enabled, dmlToolsSet: true}
}

// UnmarshalYAML preserves whether dmlTools was omitted so ApplyDefaults can
// distinguish the safe default from an explicit false.
func (f *MCPFlags) UnmarshalYAML(node *yaml.Node) error {
	type plain MCPFlags
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*f = MCPFlags(decoded)
	f.dmlToolsSet = yamlKeyPresent(node, "dmlTools")
	return nil
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
	DescribeEntities    bool `yaml:"describeEntities" json:"describeEntities"`
	ReadRecords         bool `yaml:"readRecords" json:"readRecords"`
	CreateRecord        bool `yaml:"createRecord" json:"createRecord"`
	UpdateRecord        bool `yaml:"updateRecord" json:"updateRecord"`
	DeleteRecord        bool `yaml:"deleteRecord" json:"deleteRecord"`
	ExecuteEntity       bool `yaml:"executeEntity" json:"executeEntity"`
	AggregateRecords    bool `yaml:"aggregateRecords" json:"aggregateRecords"`
	BeginTransaction    bool `yaml:"beginTransaction" json:"beginTransaction"`
	CommitTransaction   bool `yaml:"commitTransaction" json:"commitTransaction"`
	RollbackTransaction bool `yaml:"rollbackTransaction" json:"rollbackTransaction"`
	present             bool
}

// ExplicitToolFlags marks a programmatically constructed tools block present,
// including the meaningful all-false configuration.
func ExplicitToolFlags(flags ToolFlags) ToolFlags {
	flags.present = true
	return flags
}

// UnmarshalYAML records that the tools node was present.
func (f *ToolFlags) UnmarshalYAML(node *yaml.Node) error {
	type plain ToolFlags
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*f = ToolFlags(decoded)
	f.present = true
	return nil
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
	TTL     time.Duration `yaml:"ttl" json:"ttl"`
	MaxOpen int           `yaml:"maxOpen" json:"maxOpen"`
}

// CostConfig configures the defense-in-depth cost gate. SoftScore/HardScore are
// the 0-100 normalized thresholds (from cost.ScorePlan) for soft/hard reject.
type CostConfig struct {
	Enabled           *bool         `yaml:"enabled" json:"enabled"`
	SoftScore         int           `yaml:"softScore" json:"softScore"`
	HardScore         int           `yaml:"hardScore" json:"hardScore"`
	MaxRows           int64         `yaml:"maxRows" json:"maxRows"`
	MaxBytes          int64         `yaml:"maxBytes" json:"maxBytes"`
	RejectFullScan    bool          `yaml:"rejectFullScan" json:"rejectFullScan"`
	WhitelistPKPoint  bool          `yaml:"whitelistPKPoint" json:"whitelistPKPoint"`
	RequirePKForWrite *bool         `yaml:"requirePKForWrite" json:"requirePKForWrite"`
	RequireKnownScan  bool          `yaml:"requireKnownScan" json:"requireKnownScan"`
	RequireFreshStats bool          `yaml:"requireFreshStats" json:"requireFreshStats"`
	QueryTimeout      time.Duration `yaml:"queryTimeout" json:"queryTimeout"`
	AllowTemplates    []string      `yaml:"allowTemplates" json:"allowTemplates"`
	RejectTemplates   []string      `yaml:"rejectTemplates" json:"rejectTemplates"`
	AQE               AQEConfig     `yaml:"aqe" json:"aqe"`
	present           map[string]bool
}

// UnmarshalYAML tracks each cost field independently so partial configuration
// still receives defaults while explicit zero/false values are preserved.
func (c *CostConfig) UnmarshalYAML(node *yaml.Node) error {
	type plain CostConfig
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*c = CostConfig(decoded)
	c.present = make(map[string]bool)
	for i := 0; i+1 < len(node.Content); i += 2 {
		c.present[node.Content[i].Value] = true
	}
	return nil
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
	WindowSize        int           `yaml:"windowSize" json:"windowSize"`
	AnomalyFactor     float64       `yaml:"anomalyFactor" json:"anomalyFactor"`
	AnomalyMinSamples int           `yaml:"anomalyMinSamples" json:"anomalyMinSamples"`
	ExplainAnalyze    bool          `yaml:"explainAnalyze" json:"explainAnalyze"`
	ReadOnly          bool          `yaml:"readOnly" json:"readOnly"`
	SampleRate        float64       `yaml:"sampleRate" json:"sampleRate"`
	Timeout           time.Duration `yaml:"timeout" json:"timeout"`
	present           map[string]bool
}

// UnmarshalYAML tracks safety fields so omitted readOnly/timeout values can
// receive secure defaults while explicit false/zero values remain visible.
func (c *AQEConfig) UnmarshalYAML(node *yaml.Node) error {
	type plain AQEConfig
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*c = AQEConfig(decoded)
	c.present = make(map[string]bool)
	for i := 0; i+1 < len(node.Content); i += 2 {
		c.present[node.Content[i].Value] = true
	}
	return nil
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
	Roles   map[string]BudgetLimits `yaml:"roles" json:"roles"`
	Tenants map[string]BudgetLimits `yaml:"tenants" json:"tenants"`
}

// BudgetLimits is unlimited when all fields are zero.
type BudgetLimits struct {
	MaxConcurrent   int           `yaml:"maxConcurrent" json:"maxConcurrent"`
	MaxExecution    time.Duration `yaml:"maxExecution" json:"maxExecution"`
	MaxScannedRows  int64         `yaml:"maxScannedRows" json:"maxScannedRows"`
	MaxReturnedRows int64         `yaml:"maxReturnedRows" json:"maxReturnedRows"`
	MaxSessionCost  int64         `yaml:"maxSessionCost" json:"maxSessionCost"`
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

func yamlKeyPresent(node *yaml.Node, key string) bool {
	if node.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return true
		}
	}
	return false
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
	Enabled         bool          `yaml:"enabled" json:"enabled"`
	TTL             time.Duration `yaml:"ttl" json:"ttl"`
	MaxSize         int           `yaml:"maxSize" json:"maxSize"`
	PreparedMaxSize int           `yaml:"preparedMaxSize" json:"preparedMaxSize"`
}

// RateLimitConfig configures the engine's concurrency and rate limits.
type RateLimitConfig struct {
	Enabled          *bool         `yaml:"enabled" json:"enabled"`
	RPS              float64       `yaml:"rps" json:"rps"`
	MaxInflight      int           `yaml:"maxInflight" json:"maxInflight"`
	IOPool           int           `yaml:"ioPool" json:"ioPool"`
	CPUPool          int           `yaml:"cpuPool" json:"cpuPool"`
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
	Enabled   bool   `yaml:"enabled" json:"enabled"`
	Path      string `yaml:"path" json:"path"`
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

// ApplyDefaults fills unset fields with safe defaults. An omitted Tools section
// receives DefaultToolFlags; entities with unset MCP get DMLTools=true; each
// omitted cost safety field receives its own default; rate limits default to
// conservative bounds.
func (c *Config) ApplyDefaults() {
	if len(c.Databases) == 0 && c.Database.Driver != "" {
		c.Databases = map[string]DatabaseConfig{"default": c.Database}
	}
	if !c.Tools.present && c.Tools == (ToolFlags{}) {
		c.Tools = DefaultToolFlags()
	}
	for i := range c.Entities {
		if !c.Entities[i].MCP.dmlToolsSet {
			c.Entities[i].MCP.DMLTools = true
		}
	}
	if c.Cost.EnabledOrDefault() {
		// Scores are 0-100 where higher is safer; apply every safety default
		// independently so setting one field cannot erase the others.
		if !c.Cost.present["softScore"] && c.Cost.SoftScore == 0 {
			c.Cost.SoftScore = 60
		}
		if !c.Cost.present["hardScore"] && c.Cost.HardScore == 0 {
			c.Cost.HardScore = 40
		}
		if !c.Cost.present["maxRows"] && c.Cost.MaxRows == 0 {
			c.Cost.MaxRows = 10000
		}
		if !c.Cost.present["whitelistPKPoint"] {
			c.Cost.WhitelistPKPoint = true
		}
	}
	if !c.Cost.present["queryTimeout"] && c.Cost.QueryTimeout == 0 {
		c.Cost.QueryTimeout = 30 * time.Second
	}
	if c.Cost.AQE.WindowSize == 0 {
		c.Cost.AQE.WindowSize = 32
	}
	if c.Cost.AQE.AnomalyFactor == 0 {
		c.Cost.AQE.AnomalyFactor = 3
	}
	if c.Cost.AQE.AnomalyMinSamples == 0 {
		c.Cost.AQE.AnomalyMinSamples = 5
	}
	if !c.Cost.AQE.present["readOnly"] {
		c.Cost.AQE.ReadOnly = true
	}
	if !c.Cost.AQE.present["timeout"] && c.Cost.AQE.Timeout == 0 {
		c.Cost.AQE.Timeout = time.Second
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
	if c.Transactions.TTL == 0 {
		c.Transactions.TTL = 5 * time.Minute
	}
	if c.Transactions.MaxOpen == 0 {
		c.Transactions.MaxOpen = 128
	}
}

// Validate checks required fields. DSN placeholder resolution happens later in
// x/bootstrap; here we only require non-empty.
func (c *Config) Validate() error {
	databases := c.Databases
	if len(databases) == 0 && c.Database.Driver != "" {
		databases = map[string]DatabaseConfig{"default": c.Database}
	}
	if len(databases) == 0 {
		return ErrEmptyDSN
	}
	if (c.Server.Auth.TLS.Cert == "") != (c.Server.Auth.TLS.Key == "") {
		return fmt.Errorf("config: tls.cert and tls.key must be configured together")
	}
	if c.Server.Auth.TrustProxyHeaders && c.Server.Auth.TLS.ClientCA == "" && len(c.Server.Auth.TrustedProxyCIDRs) == 0 {
		return fmt.Errorf("config: trustProxyHeaders requires mTLS clientCA or trustedProxyCIDRs")
	}
	for _, cidr := range c.Server.Auth.TrustedProxyCIDRs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("config: invalid trusted proxy CIDR %q: %w", cidr, err)
		}
	}
	for name, db := range databases {
		switch db.Driver {
		case "postgres", "mysql", "oceanbase":
		default:
			return fmt.Errorf("%w: %q in database %q", ErrInvalidDriver, db.Driver, name)
		}
		if db.DSN == "" {
			return fmt.Errorf("%w: database %q", ErrEmptyDSN, name)
		}
	}
	datasources := make([]string, 0, len(databases))
	for name := range databases {
		datasources = append(datasources, name)
	}
	if err := cost.ValidateTemplateScopes(datasources, c.Cost.AllowTemplates, c.Cost.RejectTemplates); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if c.Cost.SoftScore < 0 || c.Cost.SoftScore > 100 {
		return fmt.Errorf("config: cost softScore must be between 0 and 100")
	}
	if c.Cost.HardScore < 0 || c.Cost.HardScore > 100 {
		return fmt.Errorf("config: cost hardScore must be between 0 and 100")
	}
	if c.Cost.SoftScore < c.Cost.HardScore {
		return fmt.Errorf("config: cost softScore must be greater than or equal to hardScore")
	}
	if math.IsNaN(c.Cost.AQE.SampleRate) || c.Cost.AQE.SampleRate < 0 || c.Cost.AQE.SampleRate > 1 {
		return fmt.Errorf("config: cost aqe sampleRate must be between 0 and 1")
	}
	if c.Cost.AQE.Timeout < 0 || c.Cost.AQE.Timeout > 5*time.Second {
		return fmt.Errorf("config: cost aqe timeout must be between 0 and 5s")
	}
	if c.Cost.AQE.ExplainAnalyze {
		if !c.Cost.EnabledOrDefault() {
			return fmt.Errorf("config: cost aqe EXPLAIN ANALYZE requires cost.enabled")
		}
		if !c.Cost.AQE.ReadOnly {
			return fmt.Errorf("config: cost aqe EXPLAIN ANALYZE requires readOnly: true")
		}
		if c.Cost.AQE.Timeout == 0 {
			return fmt.Errorf("config: cost aqe timeout must be greater than 0 when EXPLAIN ANALYZE is enabled")
		}
		for name, db := range databases {
			if db.Driver != "postgres" {
				return fmt.Errorf("config: database %q driver %q does not support EXPLAIN ANALYZE sampling", name, db.Driver)
			}
		}
	}
	if c.Audit.Enabled && c.Audit.Path == "" {
		return fmt.Errorf("config: audit path is required when audit is enabled")
	}
	if c.RateLimit.IOPool < 0 || c.RateLimit.CPUPool < 0 {
		return fmt.Errorf("config: rate-limit pools must not be negative")
	}
	if c.Cost.QueryTimeout < 0 || c.Cache.TTL < 0 || c.RateLimit.RTTThreshold < 0 ||
		c.RateLimit.BreakerCooldown < 0 || c.RateLimit.ConnMaxIdleTime < 0 || c.Transactions.TTL < 0 {
		return fmt.Errorf("config: timeouts must not be negative")
	}
	if c.Transactions.MaxOpen < 0 {
		return fmt.Errorf("config: transactions maxOpen must not be negative")
	}
	for scope, limits := range c.Budget.Roles {
		if err := validateBudgetLimits(limits); err != nil {
			return fmt.Errorf("config: role budget %q: %w", scope, err)
		}
	}
	for scope, limits := range c.Budget.Tenants {
		if err := validateBudgetLimits(limits); err != nil {
			return fmt.Errorf("config: tenant budget %q: %w", scope, err)
		}
	}
	entitySources := make(map[string]string, len(c.Entities))
	entityConfigs := make(map[string]EntityConfig, len(c.Entities))
	for _, e := range c.Entities {
		if e.Name == "" {
			return ErrEmptyEntityName
		}
		if _, exists := entityConfigs[e.Name]; exists {
			return fmt.Errorf("config: duplicate entity name %q", e.Name)
		}
		source := e.DataSource
		if source == "" {
			source = "default"
		}
		if _, ok := databases[source]; !ok {
			return fmt.Errorf("config: entity %q references unknown datasource %q", e.Name, source)
		}
		entitySources[e.Name] = source
		entityConfigs[e.Name] = e
	}
	for _, e := range c.Entities {
		if duplicate, ok := firstDuplicateField(e.Fields); ok {
			return fmt.Errorf("config: entity %q has duplicate field %q", e.Name, duplicate)
		}
		if duplicate, ok := firstDuplicate(e.Params); ok {
			return fmt.Errorf("config: entity %q has duplicate parameter %q", e.Name, duplicate)
		}
		visibleFields := make(map[string]bool, len(e.Fields)*2)
		for _, field := range e.Fields {
			if field.Exclude {
				continue
			}
			visibleFields[field.Name] = true
			if field.Alias != "" {
				visibleFields[field.Alias] = true
			}
		}
		for role, acl := range e.FieldACL {
			if duplicate, ok := firstDuplicate(acl.Read); ok {
				return fmt.Errorf("config: entity %q field ACL for role %q has duplicate read field %q", e.Name, role, duplicate)
			}
			if duplicate, ok := firstDuplicate(acl.Write); ok {
				return fmt.Errorf("config: entity %q field ACL for role %q has duplicate write field %q", e.Name, role, duplicate)
			}
			for _, field := range append(append([]string{}, acl.Read...), acl.Write...) {
				if !visibleFields[field] {
					return fmt.Errorf("config: entity %q field ACL for role %q references unknown or excluded field %q", e.Name, role, field)
				}
			}
		}
		for role, policy := range e.RowPolicies {
			if err := validateRowPolicy(policy); err != nil {
				return fmt.Errorf("config: entity %q row policy for role %q: %w", e.Name, role, err)
			}
		}
		relationNames := make(map[string]bool, len(e.Relationships))
		localFields := configuredFields(e.Fields)
		for _, relation := range e.Relationships {
			if relation.Name == "" {
				return fmt.Errorf("config: entity %q has relationship with empty name", e.Name)
			}
			if relationNames[relation.Name] {
				return fmt.Errorf("config: entity %q has duplicate relationship %q", e.Name, relation.Name)
			}
			relationNames[relation.Name] = true
			targetSource, ok := entitySources[relation.Target]
			if !ok {
				return fmt.Errorf("config: entity %q relationship %q references unknown target %q", e.Name, relation.Name, relation.Target)
			}
			source := e.DataSource
			if source == "" {
				source = "default"
			}
			if targetSource != source {
				return fmt.Errorf("config: cross-datasource relationship %q is not supported", relation.Name)
			}
			switch relation.Cardinality {
			case "one", "one-to-one", "belongs-to", "many", "one-to-many", "has-many":
			default:
				return fmt.Errorf("config: relationship %q has invalid cardinality %q", relation.Name, relation.Cardinality)
			}
			if len(relation.JoinOn) != 1 {
				return fmt.Errorf("config: relationship %q requires exactly one joinOn pair", relation.Name)
			}
			targetFields := configuredFields(entityConfigs[relation.Target].Fields)
			for local, target := range relation.JoinOn {
				if !localFields[local] {
					return fmt.Errorf("config: relationship %q references unknown local field %q", relation.Name, local)
				}
				if !targetFields[target] {
					return fmt.Errorf("config: relationship %q references unknown target field %q", relation.Name, target)
				}
			}
		}
	}
	return nil
}

func configuredFields(fields []FieldConfig) map[string]bool {
	out := make(map[string]bool, len(fields)*2)
	for _, field := range fields {
		out[field.Name] = true
		if field.Alias != "" {
			out[field.Alias] = true
		}
	}
	return out
}

func validateBudgetLimits(limits BudgetLimits) error {
	if limits.MaxConcurrent < 0 || limits.MaxExecution < 0 || limits.MaxScannedRows < 0 ||
		limits.MaxReturnedRows < 0 || limits.MaxSessionCost < 0 {
		return fmt.Errorf("limits must not be negative")
	}
	return nil
}

func firstDuplicateField(fields []FieldConfig) (string, bool) {
	seen := make(map[string]bool, len(fields))
	for _, field := range fields {
		if seen[field.Name] {
			return field.Name, true
		}
		seen[field.Name] = true
	}
	return "", false
}

func firstDuplicate(values []string) (string, bool) {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if seen[value] {
			return value, true
		}
		seen[value] = true
	}
	return "", false
}

func validateRowPolicy(policy FilterConfig) error {
	if op, ok := policy["op"]; ok {
		name, ok := op.(string)
		if !ok || !relalg.Op(name).Valid() {
			return fmt.Errorf("invalid operator %q", op)
		}
		return nil
	}
	for _, key := range []string{"and", "or"} {
		if raw, ok := policy[key]; ok {
			items, ok := raw.([]any)
			if !ok {
				return fmt.Errorf("%s must be a list", key)
			}
			for _, item := range items {
				child, ok := item.(map[string]any)
				if !ok {
					return fmt.Errorf("%s item must be an object", key)
				}
				if err := validateRowPolicy(child); err != nil {
					return err
				}
			}
			return nil
		}
	}
	return fmt.Errorf("missing op/and/or")
}
