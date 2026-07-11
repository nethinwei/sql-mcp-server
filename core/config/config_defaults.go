package config

import (
	"runtime"
	"strings"
	"time"
)

// ApplyDefaults fills unset fields with safe defaults. An omitted Tools section
// receives DefaultToolFlags; entities with unset MCP get DMLTools=true; each
// omitted cost safety field receives its own default; rate limits default to
// conservative bounds.
func (c *Config) ApplyDefaults() {
	c.applyServerDefaults()
	c.applyDatabaseDefaults()
	c.applyToolsDefaults()
	c.applyEntityDefaults()
	c.applyCostDefaults()
	c.applyAQEDefaults()
	c.applyRateLimitDefaults()
	c.applyAuditDefaults()
	c.applyCacheDefaults()
	c.applyTransactionDefaults()
}

func (c *Config) applyServerDefaults() {
	c.Server.Role = canonicalRole(c.Server.Role)
	if len(c.Server.Secrets.AllowedRoots) == 0 {
		c.Server.Secrets.AllowedRoots = []string{"/run/secrets", "/var/run/secrets"}
	}
	if c.Server.Transport == "" {
		c.Server.Transport = "stdio"
	}
}

func (c *Config) applyDatabaseDefaults() {
	if len(c.Databases) == 0 && c.Database.Driver != "" {
		c.Databases = map[string]DatabaseConfig{"default": c.Database}
	}
}

func (c *Config) applyToolsDefaults() {
	if !c.Tools.present && c.Tools == (ToolFlags{}) {
		c.Tools = DefaultToolFlags()
	}
}

func (c *Config) applyEntityDefaults() {
	for i := range c.Entities {
		if !c.Entities[i].MCP.dmlToolsSet {
			c.Entities[i].MCP.DMLTools = true
		}
	}
}

func (c *Config) applyCostDefaults() {
	if !c.Cost.present["maxRows"] && c.Cost.MaxRows == 0 {
		c.Cost.MaxRows = 10000
	}
	if !c.Cost.present["maxBytes"] && c.Cost.MaxBytes == 0 {
		c.Cost.MaxBytes = 16 << 20
	}
	if c.Cost.MaxINListSize == 0 {
		c.Cost.MaxINListSize = 256
	}
	if c.Cost.MaxFilterConditions == 0 {
		c.Cost.MaxFilterConditions = 32
	}
	if c.Cost.MaxGroupByFields == 0 {
		c.Cost.MaxGroupByFields = 8
	}
	if c.Cost.MaxAggregates == 0 {
		c.Cost.MaxAggregates = 16
	}
	if c.Cost.MaxExpand == 0 {
		c.Cost.MaxExpand = 8
	}
	if c.Cost.MaxProcedureRows == 0 {
		c.Cost.MaxProcedureRows = 1000
	}
	c.applyCostSafetyDefaults()
	if !c.Cost.present["queryTimeout"] && c.Cost.QueryTimeout == 0 {
		c.Cost.QueryTimeout = 30 * time.Second
	}
}

func (c *Config) applyCostSafetyDefaults() {
	if !c.Cost.EnabledOrDefault() {
		return
	}
	// Scores are 0-100 where higher is safer; apply every safety default
	// independently so setting one field cannot erase the others.
	if !c.Cost.present["softScore"] && c.Cost.SoftScore == 0 {
		c.Cost.SoftScore = 60
	}
	if !c.Cost.present["hardScore"] && c.Cost.HardScore == 0 {
		c.Cost.HardScore = 40
	}
	if !c.Cost.present["whitelistPKPoint"] {
		c.Cost.WhitelistPKPoint = true
	}
	if !c.Cost.present["rejectFullScan"] {
		c.Cost.RejectFullScan = true
	}
	if !c.Cost.present["requireKnownScan"] {
		c.Cost.RequireKnownScan = true
	}
}

func (c *Config) applyAQEDefaults() {
	if c.Cost.AQE.WindowSize == 0 {
		c.Cost.AQE.WindowSize = 32
	}
	if c.Cost.AQE.AnomalyFactor == 0 {
		c.Cost.AQE.AnomalyFactor = 3
	}
	if c.Cost.AQE.AnomalyMinSamples == 0 {
		c.Cost.AQE.AnomalyMinSamples = 5
	}
	if c.Cost.AQE.MaxFingerprints == 0 {
		c.Cost.AQE.MaxFingerprints = 4096
	}
	if !c.Cost.AQE.present["readOnly"] {
		c.Cost.AQE.ReadOnly = true
	}
	if !c.Cost.AQE.present["timeout"] && c.Cost.AQE.Timeout == 0 {
		c.Cost.AQE.Timeout = time.Second
	}
}

func (c *Config) applyRateLimitDefaults() {
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
	if c.RateLimit.ConnMaxLifetime == 0 {
		c.RateLimit.ConnMaxLifetime = 30 * time.Minute
	}
}

func (c *Config) applyAuditDefaults() {
	if c.Audit.QueueSize == 0 {
		c.Audit.QueueSize = 1024
	}
}

func (c *Config) applyCacheDefaults() {
	if c.Cache.Enabled && c.Cache.TTL == 0 {
		c.Cache.TTL = 30 * time.Second
	}
	if c.Cache.Enabled && c.Cache.MaxSize == 0 {
		c.Cache.MaxSize = 4096
	}
	if c.Cache.Enabled && c.Cache.MaxEntryRows == 0 {
		c.Cache.MaxEntryRows = int(c.Cost.MaxRows)
	}
	if c.Cache.Enabled && c.Cache.MaxEntryBytes == 0 {
		c.Cache.MaxEntryBytes = c.Cost.MaxBytes
	}
}

func (c *Config) applyTransactionDefaults() {
	if c.Transactions.TTL == 0 {
		c.Transactions.TTL = 5 * time.Minute
	}
	if c.Transactions.MaxOpen == 0 {
		c.Transactions.MaxOpen = 128
	}
	if c.Transactions.BeginTimeout == 0 {
		c.Transactions.BeginTimeout = 5 * time.Second
	}
	if c.Transactions.CommitTimeout == 0 {
		c.Transactions.CommitTimeout = 30 * time.Second
	}
	if c.Transactions.RollbackTimeout == 0 {
		c.Transactions.RollbackTimeout = 30 * time.Second
	}
}

func canonicalRole(role string) string {
	return strings.ToLower(strings.TrimSpace(role))
}

func canonicalRoleList(values []string) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = canonicalRole(value)
	}
	return out
}
