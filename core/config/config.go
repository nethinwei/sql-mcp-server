package config

import (
	"errors"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/cost"
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
	Version      string                    `yaml:"version"         json:"version"`
	Server       ServerConfig              `yaml:"server"          json:"server"`
	Database     DatabaseConfig            `yaml:"database"        json:"database" schema:"nodefault"`
	Databases    map[string]DatabaseConfig `yaml:"databases"       json:"databases" schema:"nodefault,keys=@pathSegment"`
	Entities     []EntityConfig            `yaml:"entities"        json:"entities"`
	Roles        map[string]RoleDefinition `yaml:"roles,omitempty" json:"roles,omitempty" schema:"keys=@accessName"`
	Users        map[string]UserConfig     `yaml:"users,omitempty" json:"users,omitempty" schema:"keys=@accessName"`
	Tools        ToolFlags                 `yaml:"tools"           json:"tools" schema:"nodefault"`
	Cost         CostConfig                `yaml:"cost"            json:"cost"`
	Budget       BudgetConfig              `yaml:"budget"          json:"budget"`
	Cache        CacheConfig               `yaml:"cache"           json:"cache"`
	RateLimit    RateLimitConfig           `yaml:"rateLimit"       json:"rateLimit"`
	Mask         MaskConfig                `yaml:"mask"            json:"mask"`
	Audit        AuditConfig               `yaml:"audit"           json:"audit"`
	Transactions TransactionConfig         `yaml:"transactions"    json:"transactions"`
	Execution    ExecutionConfig           `yaml:"execution"       json:"execution"`
}

// ServerConfig holds transport and role settings.
type ServerConfig struct {
	Transport string `yaml:"transport" json:"transport" schema:"enum=@transport,restart"`
	// Addr is the HTTP listen address.
	Addr string `yaml:"addr" json:"addr" schema:"restart"`
	// PathPrefix is the path a gateway serves this service under and strips
	// from requests, such as /sql-mcp-server; the console's pages, cookie and
	// redirects carry it, so the gateway need not rewrite responses.
	PathPrefix string `yaml:"pathPrefix,omitempty" json:"pathPrefix,omitempty" schema:"pattern=@pathPrefix,restart"`
	// PublicURL is the MCP endpoint as clients reach it, through any proxy;
	// the console builds client configuration with it.
	PublicURL string `yaml:"publicURL,omitempty" json:"publicURL,omitempty" schema:"pattern=@httpURL"`
	// Role is the runtime role; the --role flag overrides it.
	Role string `yaml:"role" json:"role"`
	// Auth configures HTTP transport authentication.
	Auth    AuthConfig    `yaml:"auth"    json:"auth"`
	Secrets SecretsConfig `yaml:"secrets" json:"secrets"`

	// User is the default user for requests without a user identity; it takes
	// precedence over Role and may be overridden by the --user flag.
	User string `yaml:"user,omitempty" json:"user,omitempty"`
	// SessionTimeout closes an HTTP MCP session idle this long.
	SessionTimeout time.Duration `yaml:"sessionTimeout" json:"sessionTimeout" schema:"min=0,restart"`
}

// SecretsConfig restricts ${file:...} expansion to explicitly trusted roots.
type SecretsConfig struct {
	AllowedRoots []string `yaml:"allowedRoots" json:"allowedRoots"`
}

// AuthConfig configures HTTP transport authentication. When the listener is not
// bound to loopback, at least one of Token or TLS.ClientCA (mTLS) must be
// configured or startup is refused (fail-closed). The caller identity headers
// (X-MCP-User / X-MCP-Role / X-MCP-Subject) are trusted only when
// TrustProxyHeaders is true, and only from a proxy: a source address in
// TrustedProxyCIDRs or, under mTLS, a client certificate whose subject is in
// TrustedProxyCertSubjects. Holding a certificate the client CA signed does
// not make a caller a proxy.
type AuthConfig struct {
	Token                    string    `yaml:"token"                    json:"token"`
	TrustProxyHeaders        bool      `yaml:"trustProxyHeaders"        json:"trustProxyHeaders"`
	TrustedProxyCIDRs        []string  `yaml:"trustedProxyCIDRs"        json:"trustedProxyCIDRs"`
	TrustedProxyCertSubjects []string  `yaml:"trustedProxyCertSubjects" json:"trustedProxyCertSubjects"`
	TLS                      TLSConfig `yaml:"tls"                      json:"tls"`
}

// TLSConfig configures TLS/mTLS for the HTTP transport. Cert+Key enable TLS;
// setting ClientCA additionally requires and verifies a client certificate.
type TLSConfig struct {
	Cert     string `yaml:"cert"     json:"cert"`
	Key      string `yaml:"key"      json:"key"`
	ClientCA string `yaml:"clientCA" json:"clientCA"`
	// ClientIdentity is who a client certificate without a bearer token
	// calls as when users are configured: the user its certSubjects name
	// ("user", refused when none does) or the default identity ("default").
	ClientIdentity string `yaml:"clientIdentity" json:"clientIdentity" schema:"enum=@clientIdentity"`
}

// Values of TLSConfig.ClientIdentity.
const (
	ClientIdentityUser    = "user"
	ClientIdentityDefault = "default"
)

// DatabaseConfig is one database (a datasource): one connection given by
// DSN, or several connections, such as accounts with different privileges or
// read replicas, with Routing choosing one per kind of action. DSNs may
// contain ${ENV} or ${file:/path} placeholders resolved by x/bootstrap.
type DatabaseConfig struct {
	Driver      string        `yaml:"driver"                json:"driver" schema:"required,pattern=@driver"`
	DSN         string        `yaml:"dsn,omitempty"         json:"dsn,omitempty"`
	Connections Connections   `yaml:"connections,omitempty" json:"connections,omitempty" schema:"keys=@accessName"`
	Routing     RoutingConfig `yaml:"routing,omitempty"     json:"routing,omitempty"`
	// ReadAfterWrite routes a session's reads to the write connection for
	// this long after it writes, so it reads its own writes on a replica.
	ReadAfterWrite time.Duration `yaml:"readAfterWrite,omitempty" json:"readAfterWrite,omitempty" schema:"min=0,max=1h"`
	// SQLEscape, when set, admits the trusted SQL escape hatch (execute_sql)
	// to this database on the connection it names; unset, no user may run
	// SQL on it whatever their permissions. It hot-reloads like the other
	// connection settings.
	SQLEscape SQLEscapeConfig `yaml:"sqlEscape,omitempty" json:"sqlEscape,omitempty"`
}

// SQLEscapeConfig declares the connection the SQL escape hatch runs on. The
// account of that connection bounds what the hatch can do: the server runs
// the SQL as it is, without parsing, rewriting or masking.
type SQLEscapeConfig struct {
	Connection string `yaml:"connection" json:"connection" schema:"required,minLength=1"`
}

// Enabled reports whether the database admits the escape hatch.
func (s SQLEscapeConfig) Enabled() bool { return s.Connection != "" }

// Connections are a database's connections by name.
type Connections map[string]ConnectionConfig

// ConnectionConfig is one way to reach a database: an endpoint and account.
type ConnectionConfig struct {
	DSN string `yaml:"dsn" json:"dsn" schema:"required,minLength=1"`
	// Role is primary (the default) or replica; a replica serves reads only.
	Role string `yaml:"role,omitempty" json:"role,omitempty" schema:"enum=@connectionRole"`
	// Pooler is "transaction" behind a transaction-mode pooler (pgbouncer,
	// ProxySQL): no session state such as prepared statements is kept.
	Pooler string `yaml:"pooler,omitempty" json:"pooler,omitempty" schema:"enum=@pooler"`
}

// RoutingConfig names the connection each kind of action uses: Read for
// reads, aggregates, EXPLAIN and read-only transactions; Write for creates,
// updates, deletes and read-write transactions; Execute for procedures.
type RoutingConfig struct {
	Read    string `yaml:"read,omitempty"    json:"read,omitempty"`
	Write   string `yaml:"write,omitempty"   json:"write,omitempty"`
	Execute string `yaml:"execute,omitempty" json:"execute,omitempty"`
}

// DefaultConnection names the single connection a DSN shorthand declares.
const DefaultConnection = "default"

// ConnectionsOrDSN returns the database's connections: the DSN shorthand as
// one primary connection named DefaultConnection, or Connections. A
// configuration built in code may hold either until ApplyDefaults.
func (d DatabaseConfig) ConnectionsOrDSN() Connections {
	if len(d.Connections) > 0 {
		return d.Connections
	}
	return Connections{DefaultConnection: {DSN: d.DSN}}
}

// Route returns the connections for reads, writes and procedure calls; a
// single connection without routing serves every route.
func (d DatabaseConfig) Route() RoutingConfig {
	connections := d.ConnectionsOrDSN()
	if len(connections) == 1 && d.Routing == (RoutingConfig{}) {
		for name := range connections {
			return RoutingConfig{Read: name, Write: name, Execute: name}
		}
	}
	return d.Routing
}

// FilterConfig is a declarative filter (JSON object) for row-level policies,
// converted to a relalg.Predicate by x/bootstrap.
type FilterConfig = map[string]any

// RowPolicies maps a role name to its row-level filter.
type RowPolicies map[string]FilterConfig

// EntityConfig is the configuration view of one entity. Kind is table, view or
// procedure.
type EntityConfig struct {
	Name          string                    `yaml:"name" json:"name" schema:"required,minLength=1,pattern=@pathSegment"`
	Source        string                    `yaml:"source,omitempty"        json:"source,omitempty"`
	DataSource    string                    `yaml:"datasource,omitempty"    json:"datasource,omitempty"`
	Schema        string                    `yaml:"schema,omitempty" json:"schema,omitempty" schema:"pattern=@pathSegment"`
	Kind          string                    `yaml:"kind,omitempty" json:"kind,omitempty" schema:"enum=@entityKind"`
	Description   string                    `yaml:"description,omitempty"   json:"description,omitempty"`
	PrimaryKey    []string                  `yaml:"primaryKey,omitempty"    json:"primaryKey,omitempty"`
	UniqueKeys    [][]string                `yaml:"uniqueKeys,omitempty"    json:"uniqueKeys,omitempty"`
	Fields        []FieldConfig             `yaml:"fields,omitempty"        json:"fields,omitempty"`
	Roles         RoleConfig                `yaml:"roles,omitempty"         json:"roles,omitempty"`
	FieldACL      map[string]FieldACLConfig `yaml:"fieldACL,omitempty"      json:"fieldACL,omitempty"`
	MCP           MCPFlags                  `yaml:"mcp"                     json:"mcp"`
	RowPolicies   RowPolicies               `yaml:"rowPolicies,omitempty"   json:"rowPolicies,omitempty"`
	Relationships []RelationshipConfig      `yaml:"relationships,omitempty" json:"relationships,omitempty"`
	// TenantPolicy is ANDed for every principal and never merged across grants.
	TenantPolicy FilterConfig `yaml:"tenantPolicy,omitempty"  json:"tenantPolicy,omitempty"`
	// Params is the ordered formal-parameter list for a procedure entity, bound
	// positionally by execute_entity. Required for procedures.
	Params []string `yaml:"params,omitempty"        json:"params,omitempty"`
	// Affects lists the entities a procedure writes, for cache invalidation;
	// empty invalidates every cached read of its datasource.
	Affects []string `yaml:"affects,omitempty"       json:"affects,omitempty"`
	// AllowCascade admits deletes and key updates that foreign keys cascade
	// to other relations without the caller's permissions on them.
	AllowCascade bool `yaml:"allowCascade,omitempty"  json:"allowCascade,omitempty"`
	// AllowKeyUpdate lets update_records assign the columns that identify a
	// row; refused by default.
	AllowKeyUpdate bool `yaml:"allowKeyUpdate,omitempty" json:"allowKeyUpdate,omitempty"`
}

// PhysicalSource is the table or procedure the entity reads: Source, or the
// entity name when Source is empty.
func (e EntityConfig) PhysicalSource() string {
	if e.Source == "" {
		return e.Name
	}
	return e.Source
}

// DatasourceName is the entity's datasource: DataSource, or "default" (the
// single database section) when empty.
func (e EntityConfig) DatasourceName() string {
	if e.DataSource == "" {
		return "default"
	}
	return e.DataSource
}

// RoleDefinition is a named set of grants declared at the top level. Entity
// level roles/fieldACL/rowPolicies remain valid and merge into the same role.
type RoleDefinition struct {
	Description string        `yaml:"description,omitempty"   json:"description,omitempty"`
	Grants      []GrantConfig `yaml:"grants,omitempty"      json:"grants,omitempty"`
	// Permissions holds system permissions: "sql:execute@<datasource>" admits
	// the role's users to the SQL escape hatch of that datasource. No other
	// permission is supported yet; any other value fails validation.
	Permissions []string `yaml:"permissions,omitempty" json:"permissions,omitempty"`
	// Limits narrow the global limits for the role's calls.
	Limits PrincipalLimits `yaml:"limits,omitempty" json:"limits,omitempty"`
}

// PrincipalLimits narrow the global limits for the calls of a role or a user.
// A field left zero keeps the global value; a set one only tightens it (the
// stricter of the two applies). A user's own fields win over its roles';
// otherwise each is the most permissive of its roles' (a role leaving it
// unset leaves it unset).
type PrincipalLimits struct {
	MaxRows        int64         `yaml:"maxRows,omitempty"        json:"maxRows,omitempty" schema:"min=0"`
	MaxBytes       int64         `yaml:"maxBytes,omitempty"       json:"maxBytes,omitempty" schema:"min=0"`
	MaxScannedRows int64         `yaml:"maxScannedRows,omitempty" json:"maxScannedRows,omitempty" schema:"min=0"`
	QueryTimeout   time.Duration `yaml:"queryTimeout,omitempty"   json:"queryTimeout,omitempty" schema:"min=0"`
	SoftScore      int           `yaml:"softScore,omitempty"      json:"softScore,omitempty" schema:"min=0,max=100"`
	HardScore      int           `yaml:"hardScore,omitempty"      json:"hardScore,omitempty" schema:"min=0,max=100"`
	RejectFullScan bool          `yaml:"rejectFullScan,omitempty" json:"rejectFullScan,omitempty"`
	RPS            float64       `yaml:"rps,omitempty"            json:"rps,omitempty" schema:"min=0"`
	MaxInflight    int           `yaml:"maxInflight,omitempty"    json:"maxInflight,omitempty" schema:"min=0"`
	// Budget is the principal's session budget; a field set wins over
	// budget.roles and budget.users.
	Budget BudgetLimits `yaml:"budget,omitempty" json:"budget,omitempty"`
}

// GrantConfig is one entity permission. A nil Fields grants every visible
// field; a present Fields behaves like a fieldACL entry. Empty Rows grants all
// rows.
type GrantConfig struct {
	Entity  string          `yaml:"entity"           json:"entity" schema:"required,minLength=1"`
	Actions []string        `yaml:"actions"          json:"actions" schema:"required,minItems=1,enum=@grantAction"`
	Fields  *FieldACLConfig `yaml:"fields,omitempty" json:"fields,omitempty"`
	Rows    FilterConfig    `yaml:"rows,omitempty"   json:"rows,omitempty"`
}

// UserConfig is one authenticated caller. TokenHash is "sha256:<hex>" of the
// bearer token; the plaintext token never appears in configuration.
type UserConfig struct {
	Description string `yaml:"description,omitempty"   json:"description,omitempty"`
	TokenHash   string `yaml:"tokenHash,omitempty"   json:"tokenHash,omitempty" schema:"pattern=@tokenHash"`
	// CertSubjects name the client certificates that authenticate the user
	// under mTLS: cn:<common name>, dns:<DNS SAN>, uri:<URI SAN, such as a
	// SPIFFE ID> or email:<email SAN>.
	CertSubjects []string       `yaml:"certSubjects,omitempty" json:"certSubjects,omitempty" schema:"pattern=@certSubject"`
	Roles        []string       `yaml:"roles,omitempty"       json:"roles,omitempty" schema:"pattern=@accessName"`
	Subject      map[string]any `yaml:"subject,omitempty"     json:"subject,omitempty"`
	Grants       []GrantConfig  `yaml:"grants,omitempty"      json:"grants,omitempty"`
	Permissions  []string       `yaml:"permissions,omitempty" json:"permissions,omitempty"`
	Disabled     bool           `yaml:"disabled,omitempty"    json:"disabled,omitempty"`
	// Limits narrow the global limits for the user's calls; set fields win
	// over its roles'.
	Limits PrincipalLimits `yaml:"limits,omitempty" json:"limits,omitempty"`
}

// RelationshipConfig configures a same-data-source batch expansion.
type RelationshipConfig struct {
	Name        string            `yaml:"name"        json:"name" schema:"required,minLength=1"`
	Target      string            `yaml:"target"      json:"target" schema:"required,minLength=1"`
	Cardinality string            `yaml:"cardinality" json:"cardinality" schema:"required,enum=@cardinality"`
	JoinOn      map[string]string `yaml:"joinOn"      json:"joinOn" schema:"required"`
}

// FieldConfig configures one column.
type FieldConfig struct {
	Name        string `yaml:"name"        json:"name" schema:"required,minLength=1"`
	Alias       string `yaml:"alias,omitempty"       json:"alias,omitempty"`
	Description string `yaml:"description,omitempty"   json:"description,omitempty"`
	Mask        string `yaml:"mask,omitempty"        json:"mask,omitempty" schema:"enum=@maskRule"`
	Exclude     bool   `yaml:"exclude,omitempty"     json:"exclude,omitempty"`
}

// RoleConfig lists allowed roles per action.
type RoleConfig struct {
	Read      []string `yaml:"read,omitempty"      json:"read,omitempty"`
	Create    []string `yaml:"create,omitempty"    json:"create,omitempty"`
	Update    []string `yaml:"update,omitempty"    json:"update,omitempty"`
	Delete    []string `yaml:"delete,omitempty"    json:"delete,omitempty"`
	Execute   []string `yaml:"execute,omitempty"   json:"execute,omitempty"`
	Aggregate []string `yaml:"aggregate,omitempty" json:"aggregate,omitempty"`
}

// FieldACLConfig restricts one role to named readable and writable fields.
// Omitting a role leaves existing entity-level authorization behavior intact.
type FieldACLConfig struct {
	Read  []string `yaml:"read,omitempty"      json:"read,omitempty"`
	Write []string `yaml:"write,omitempty" json:"write,omitempty"`
}

// MCPFlags controls entity participation in MCP. DMLTools defaults on: a
// Spec that does not set it resolves with dmlToolsSet false, and
// ApplyDefaults turns it on.
type MCPFlags struct {
	DMLTools         bool `yaml:"dmlTools"                   json:"dmlTools"`
	CustomTool       bool `yaml:"customTool,omitempty"       json:"customTool,omitempty"`
	TrustedProcedure bool `yaml:"trustedProcedure,omitempty" json:"trustedProcedure,omitempty"`
	dmlToolsSet      bool
}

// ToolFlags toggles each DML tool. present records that the tools section was
// written (see Spec), so an explicit all-false object differs from omission.
type ToolFlags struct {
	DescribeEntities bool `yaml:"describeEntities"    json:"describeEntities"`
	ReadRecords      bool `yaml:"readRecords"         json:"readRecords"`
	CreateRecord     bool `yaml:"createRecord"        json:"createRecord"`
	UpdateRecord     bool `yaml:"updateRecord"        json:"updateRecord"`
	DeleteRecord     bool `yaml:"deleteRecord"        json:"deleteRecord"`
	ExecuteEntity    bool `yaml:"executeEntity"       json:"executeEntity"`
	AggregateRecords bool `yaml:"aggregateRecords"    json:"aggregateRecords"`
	// ExecuteSQL registers execute_sql, the trusted SQL escape hatch. Off by
	// default; a user still needs sql:execute@<datasource> and the datasource
	// its sqlEscape connection.
	ExecuteSQL          bool `yaml:"executeSql" json:"executeSql"`
	BeginTransaction    bool `yaml:"beginTransaction"    json:"beginTransaction"`
	CommitTransaction   bool `yaml:"commitTransaction"   json:"commitTransaction"`
	RollbackTransaction bool `yaml:"rollbackTransaction" json:"rollbackTransaction"`
	present             bool
}

// TransactionConfig bounds explicit transaction lifetime and cardinality.
type TransactionConfig struct {
	TTL time.Duration `yaml:"ttl"             json:"ttl" schema:"min=0"`
	// IdleTimeout rolls back a transaction no statement used for this long,
	// releasing the locks and the connection it holds.
	IdleTimeout time.Duration `yaml:"idleTimeout" json:"idleTimeout" schema:"min=0"`
	MaxOpen     int           `yaml:"maxOpen"         json:"maxOpen" schema:"min=0"`
	// MaxOpenPerConnection bounds the transactions holding one database
	// connection at once, so they leave queries a share of its pool.
	MaxOpenPerConnection int           `yaml:"maxOpenPerConnection" json:"maxOpenPerConnection" schema:"min=0"`
	BeginTimeout         time.Duration `yaml:"beginTimeout"    json:"beginTimeout" schema:"min=0"`
	CommitTimeout        time.Duration `yaml:"commitTimeout"   json:"commitTimeout" schema:"min=0"`
	RollbackTimeout      time.Duration `yaml:"rollbackTimeout" json:"rollbackTimeout" schema:"min=0"`
}

// ExecutionConfig bounds the lanes of the execution bus other than tool
// calls, which rateLimit bounds. All lanes share rateLimit.ioPool; these
// lanes together hold at most half of it.
type ExecutionConfig struct {
	Admin      LaneConfig `yaml:"admin"      json:"admin"`
	Scan       LaneConfig `yaml:"scan"       json:"scan"`
	Background LaneConfig `yaml:"background" json:"background"`
}

// LaneConfig bounds one lane: work running at once and work waiting.
type LaneConfig struct {
	MaxConcurrent int `yaml:"maxConcurrent" json:"maxConcurrent" schema:"min=0"`
	MaxQueue      int `yaml:"maxQueue"      json:"maxQueue" schema:"min=0"`
}

// CostConfig configures the defense-in-depth cost gate. SoftScore/HardScore are
// the 0-100 normalized thresholds (from cost.ScorePlan) for soft/hard reject.
type CostConfig struct {
	Enabled             *bool               `yaml:"enabled,omitempty"           json:"enabled,omitempty"`
	SoftScore           int                 `yaml:"softScore"                   json:"softScore" schema:"min=0,max=100"`
	HardScore           int                 `yaml:"hardScore"                   json:"hardScore" schema:"min=0,max=100"`
	MaxRows             int64               `yaml:"maxRows"                     json:"maxRows" schema:"min=1"`
	MaxBytes            int64               `yaml:"maxBytes"                    json:"maxBytes" schema:"min=1"`
	MaxScannedRows      int64               `yaml:"maxScannedRows"              json:"maxScannedRows" schema:"min=0"`
	MaxINListSize       int                 `yaml:"maxINListSize"               json:"maxINListSize" schema:"min=1"`
	MaxFilterConditions int                 `yaml:"maxFilterConditions"         json:"maxFilterConditions" schema:"min=1"`
	MaxGroupByFields    int                 `yaml:"maxGroupByFields"            json:"maxGroupByFields" schema:"min=1"`
	MaxAggregates       int                 `yaml:"maxAggregates"               json:"maxAggregates" schema:"min=1"`
	MaxExpand           int                 `yaml:"maxExpand"                   json:"maxExpand" schema:"min=1"`
	MaxProcedureRows    int64               `yaml:"maxProcedureRows"            json:"maxProcedureRows" schema:"min=1"`
	RejectFullScan      bool                `yaml:"rejectFullScan"              json:"rejectFullScan"`
	FullScanMaxRows     int64               `yaml:"fullScanMaxRows"             json:"fullScanMaxRows" schema:"min=0"`
	WhitelistPKPoint    bool                `yaml:"whitelistPKPoint"            json:"whitelistPKPoint"`
	RequirePKForWrite   *bool               `yaml:"requirePKForWrite,omitempty" json:"requirePKForWrite,omitempty"`
	RequireKnownScan    bool                `yaml:"requireKnownScan"            json:"requireKnownScan"`
	RequireFreshStats   bool                `yaml:"requireFreshStats"           json:"requireFreshStats"`
	QueryTimeout        time.Duration       `yaml:"queryTimeout"                json:"queryTimeout" schema:"min=0"`
	RequireNativeLimits bool                `yaml:"requireNativeLimits"         json:"requireNativeLimits"`
	AllowTemplates      []string            `yaml:"allowTemplates"              json:"allowTemplates"`
	RejectTemplates     []string            `yaml:"rejectTemplates"             json:"rejectTemplates"`
	AQE                 AQEConfig           `yaml:"aqe"                         json:"aqe"`
	SQLEscape           SQLEscapeCostConfig `yaml:"sqlEscape"             json:"sqlEscape"`
	present             map[string]bool
}

// AQEConfig controls bounded estimate feedback and optional read-only
// EXPLAIN ANALYZE sampling.
type AQEConfig struct {
	WindowSize        int           `yaml:"windowSize"        json:"windowSize" schema:"min=0"`
	AnomalyFactor     float64       `yaml:"anomalyFactor"     json:"anomalyFactor" schema:"min=0"`
	AnomalyMinSamples int           `yaml:"anomalyMinSamples" json:"anomalyMinSamples" schema:"min=0"`
	ExplainAnalyze    bool          `yaml:"explainAnalyze"    json:"explainAnalyze"`
	ReadOnly          bool          `yaml:"readOnly"          json:"readOnly"`
	SampleRate        float64       `yaml:"sampleRate"        json:"sampleRate" schema:"min=0,max=1"`
	Timeout           time.Duration `yaml:"timeout"           json:"timeout" schema:"min=0,max=5s"`
	MaxFingerprints   int           `yaml:"maxFingerprints"   json:"maxFingerprints" schema:"min=1"`
	present           map[string]bool
}

// SQLEscapeCostConfig bounds the SQL escape hatch's estimate. Its hard
// limits (timeout, rows, bytes, budgets) are the governed plane's and cannot
// be switched off; only the EXPLAIN estimate is configurable.
type SQLEscapeCostConfig struct {
	// Estimate is required (the default: a statement EXPLAIN cannot estimate
	// is refused) or skip (no estimate, the hard limits alone).
	Estimate string `yaml:"estimate" json:"estimate" schema:"enum=@sqlEscapeEstimate"`
	// Metadata is allow (the default: SHOW and DESCRIBE statements, which
	// EXPLAIN cannot estimate, run under the hard limits alone) or refuse
	// (they are refused as any statement EXPLAIN cannot estimate).
	Metadata string `yaml:"metadata" json:"metadata" schema:"enum=@sqlEscapeMetadata"`
}

// MetadataAllowed reports whether the escape hatch runs metadata statements
// without an estimate.
func (c SQLEscapeCostConfig) MetadataAllowed() bool { return c.Metadata != SQLEscapeMetadataRefuse }

// Values of SQLEscapeCostConfig.Metadata.
const (
	SQLEscapeMetadataAllow  = "allow"
	SQLEscapeMetadataRefuse = "refuse"
)

// EstimateRequired reports whether the escape hatch refuses statements it
// cannot estimate.
func (c SQLEscapeCostConfig) EstimateRequired() bool { return c.Estimate != SQLEscapeEstimateSkip }

// Values of SQLEscapeCostConfig.Estimate.
const (
	SQLEscapeEstimateRequired = "required"
	SQLEscapeEstimateSkip     = "skip"
)

// BudgetConfig contains role and tenant scoped resource limits.
type BudgetConfig struct {
	Roles   map[string]BudgetLimits `yaml:"roles"   json:"roles"`
	Tenants map[string]BudgetLimits `yaml:"tenants" json:"tenants"`
	Users   map[string]BudgetLimits `yaml:"users,omitempty" json:"users,omitempty"`
}

// BudgetLimits is unlimited when all fields are zero.
type BudgetLimits struct {
	MaxConcurrent           int           `yaml:"maxConcurrent" json:"maxConcurrent" schema:"min=0"`
	MaxExecution            time.Duration `yaml:"maxExecution"             json:"maxExecution" schema:"min=0"`
	MaxEstimatedScannedRows int64         `yaml:"maxEstimatedScannedRows"  json:"maxEstimatedScannedRows" schema:"min=0"`
	MaxReturnedRows         int64         `yaml:"maxReturnedRows"          json:"maxReturnedRows" schema:"min=0"`
	MaxReturnedBytes        int64         `yaml:"maxReturnedBytes"         json:"maxReturnedBytes" schema:"min=0"`
	MaxSessionCost          int64         `yaml:"maxSessionCost"           json:"maxSessionCost" schema:"min=0"`
}

// EnabledOrDefault reports whether cost protection is on; nil means default
// true. A pointer preserves an explicit enabled: false through YAML decoding.
func (c CostConfig) EnabledOrDefault() bool {
	if c.Enabled != nil {
		return *c.Enabled
	}
	return true
}

// Threshold is the cost gate's threshold the configuration sets, before the
// datasource and dialect it applies to are named.
func (c CostConfig) Threshold() cost.Threshold {
	return cost.Threshold{
		SoftScore:                 c.SoftScore,
		HardScore:                 c.HardScore,
		MaxRows:                   c.MaxRows,
		MaxBytes:                  c.MaxBytes,
		MaxScannedRows:            c.MaxScannedRows,
		RejectFullScan:            c.RejectFullScan,
		FullScanMaxRows:           c.FullScanMaxRows,
		WhitelistPKPoint:          c.WhitelistPKPoint,
		RequirePKForWrite:         c.RequirePKForWriteOrDefault(),
		RequireAggregatePredicate: true,
		ExplainFailClosed:         c.EnabledOrDefault(),
		RequireKnownScan:          c.RequireKnownScan,
		RequireFreshStats:         c.RequireFreshStats,
		AllowTemplates:            c.AllowTemplates,
		RejectTemplates:           c.RejectTemplates,
	}
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
	TTL             time.Duration `yaml:"ttl"             json:"ttl" schema:"min=0"`
	MaxSize         int           `yaml:"maxSize"         json:"maxSize" schema:"min=0"`
	MaxEntryRows    int           `yaml:"maxEntryRows"    json:"maxEntryRows" schema:"min=0"`
	MaxEntryBytes   int64         `yaml:"maxEntryBytes"   json:"maxEntryBytes" schema:"min=0"`
	MaxTotalBytes   int64         `yaml:"maxTotalBytes"   json:"maxTotalBytes" schema:"min=0"`
	PreparedMaxSize int           `yaml:"preparedMaxSize" json:"preparedMaxSize" schema:"min=0"`
}

// RateLimitConfig configures the engine's concurrency and rate limits.
type RateLimitConfig struct {
	Enabled          *bool         `yaml:"enabled,omitempty"           json:"enabled,omitempty"`
	RPS              float64       `yaml:"rps"               json:"rps" schema:"min=0"`
	MaxInflight      int           `yaml:"maxInflight"       json:"maxInflight" schema:"min=0"`
	IOPool           int           `yaml:"ioPool"            json:"ioPool" schema:"min=0"`
	MinConcurrency   int           `yaml:"minConcurrency"    json:"minConcurrency" schema:"min=0"`
	RTTThreshold     time.Duration `yaml:"rttThreshold"      json:"rttThreshold" schema:"min=0"`
	BreakerThreshold int           `yaml:"breakerThreshold"  json:"breakerThreshold" schema:"min=0"`
	BreakerCooldown  time.Duration `yaml:"breakerCooldown"   json:"breakerCooldown" schema:"min=0"`
	ConnMaxIdleTime  time.Duration `yaml:"connMaxIdleTime"   json:"connMaxIdleTime" schema:"min=0"`
	ConnMaxLifetime  time.Duration `yaml:"connMaxLifetime"   json:"connMaxLifetime" schema:"min=0"`
	MaxResultMemory  int64         `yaml:"maxResultMemory"   json:"maxResultMemory" schema:"min=0"`
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
	Enabled *bool `yaml:"enabled,omitempty"           json:"enabled,omitempty"`
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
	Enabled   bool   `yaml:"enabled"         json:"enabled"`
	Path      string `yaml:"path"      json:"path"`
	QueueSize int    `yaml:"queueSize" json:"queueSize" schema:"min=0"`
}

// DefaultToolFlags returns the safe default tool set for untrusted agents:
// reading, aggregating, procedures and transactions; the writing tools
// (create_record, update_record, delete_record) are off until the tools
// section turns them on.
func DefaultToolFlags() ToolFlags {
	return ToolFlags{
		DescribeEntities:    true,
		ReadRecords:         true,
		CreateRecord:        false,
		UpdateRecord:        false,
		DeleteRecord:        false,
		ExecuteEntity:       true,
		AggregateRecords:    true,
		BeginTransaction:    true,
		CommitTransaction:   true,
		RollbackTransaction: true,
	}
}
