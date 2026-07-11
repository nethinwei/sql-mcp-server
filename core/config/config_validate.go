package config

import (
	"fmt"
	"math"
	"net"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

// Validate checks required fields. DSN placeholder resolution happens later in
// x/bootstrap; here we only require non-empty.
func (c *Config) Validate() error {
	if err := c.normalizeRoles(); err != nil {
		return err
	}
	databases, err := c.resolvedDatabases()
	if err != nil {
		return err
	}
	if err := c.validateServerAuth(); err != nil {
		return err
	}
	if err := c.validateDatabases(databases); err != nil {
		return err
	}
	if err := c.validateCostAndLimits(); err != nil {
		return err
	}
	if err := c.validateBudget(); err != nil {
		return err
	}
	return c.validateEntities(databases)
}

func (c *Config) resolvedDatabases() (map[string]DatabaseConfig, error) {
	databases := c.Databases
	if len(databases) == 0 && (c.Database.Driver != "" || c.Database.DSN != "") {
		databases = map[string]DatabaseConfig{"default": c.Database}
	}
	if len(databases) == 0 {
		return nil, ErrEmptyDSN
	}
	return databases, nil
}

func (c *Config) validateServerAuth() error {
	if (c.Server.Auth.TLS.Cert == "") != (c.Server.Auth.TLS.Key == "") {
		return fmt.Errorf("config: tls.cert and tls.key must be configured together")
	}
	if c.Server.Auth.TrustProxyHeaders && c.Server.Auth.TLS.ClientCA == "" &&
		len(c.Server.Auth.TrustedProxyCIDRs) == 0 {
		return fmt.Errorf("config: trustProxyHeaders requires mTLS clientCA or trustedProxyCIDRs")
	}
	for _, cidr := range c.Server.Auth.TrustedProxyCIDRs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("config: invalid trusted proxy CIDR %q: %w", cidr, err)
		}
	}
	return nil
}

func (c *Config) validateDatabases(databases map[string]DatabaseConfig) error {
	for name, db := range databases {
		if !validDriverName(db.Driver) {
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
	return nil
}

func (c *Config) validateCostAndLimits() error {
	if err := c.validateCostScores(); err != nil {
		return err
	}
	if err := c.validateCostInputs(); err != nil {
		return err
	}
	if err := c.validateAQEExplainAnalyze(); err != nil {
		return err
	}
	if c.Audit.Enabled && c.Audit.Path == "" {
		return fmt.Errorf("config: audit path is required when audit is enabled")
	}
	return c.validateRuntimeLimits()
}

func (c *Config) validateCostScores() error {
	if c.Cost.SoftScore < 0 || c.Cost.SoftScore > 100 {
		return fmt.Errorf("config: cost softScore must be between 0 and 100")
	}
	if c.Cost.HardScore < 0 || c.Cost.HardScore > 100 {
		return fmt.Errorf("config: cost hardScore must be between 0 and 100")
	}
	if c.Cost.SoftScore < c.Cost.HardScore {
		return fmt.Errorf("config: cost softScore must be greater than or equal to hardScore")
	}
	return nil
}

func (c *Config) validateCostInputs() error {
	if len(c.Cost.present) > 0 && (c.Cost.MaxRows <= 0 || c.Cost.MaxBytes <= 0 || c.Cost.MaxProcedureRows <= 0 ||
		c.Cost.MaxINListSize <= 0 || c.Cost.MaxFilterConditions <= 0 ||
		c.Cost.MaxGroupByFields <= 0 || c.Cost.MaxAggregates <= 0 || c.Cost.MaxExpand <= 0) {
		return fmt.Errorf("config: mandatory cost and input limits must be greater than zero")
	}
	if math.IsNaN(c.Cost.AQE.SampleRate) || c.Cost.AQE.SampleRate < 0 || c.Cost.AQE.SampleRate > 1 {
		return fmt.Errorf("config: cost aqe sampleRate must be between 0 and 1")
	}
	if c.Cost.AQE.Timeout < 0 || c.Cost.AQE.Timeout > 5*time.Second {
		return fmt.Errorf("config: cost aqe timeout must be between 0 and 5s")
	}
	return nil
}

func (c *Config) validateRuntimeLimits() error {
	if c.RateLimit.IOPool < 0 || c.RateLimit.CPUPool < 0 {
		return fmt.Errorf("config: rate-limit pools must not be negative")
	}
	if math.IsNaN(c.RateLimit.RPS) || c.RateLimit.RPS < 0 {
		return fmt.Errorf("config: rate-limit rps must not be negative or NaN")
	}
	if c.Cost.QueryTimeout < 0 || c.Cache.TTL < 0 || c.RateLimit.RTTThreshold < 0 ||
		c.RateLimit.BreakerCooldown < 0 || c.RateLimit.ConnMaxIdleTime < 0 ||
		c.RateLimit.ConnMaxLifetime < 0 || c.Transactions.TTL < 0 ||
		c.Transactions.BeginTimeout < 0 || c.Transactions.CommitTimeout < 0 ||
		c.Transactions.RollbackTimeout < 0 {
		return fmt.Errorf(
			"config: mandatory timeouts must be greater than zero and optional timeouts must not be negative",
		)
	}
	if c.Cache.Enabled && (c.Cache.MaxSize <= 0 || c.Cache.MaxEntryRows <= 0 || c.Cache.MaxEntryBytes <= 0) {
		return fmt.Errorf("config: enabled cache requires positive maxSize, maxEntryRows, and maxEntryBytes")
	}
	if c.Cost.AQE.MaxFingerprints < 0 {
		return fmt.Errorf("config: cost aqe maxFingerprints must be greater than zero")
	}
	if c.Transactions.MaxOpen < 0 {
		return fmt.Errorf("config: transactions maxOpen must not be negative")
	}
	return nil
}

func (c *Config) validateAQEExplainAnalyze() error {
	if !c.Cost.AQE.ExplainAnalyze {
		return nil
	}
	if !c.Cost.EnabledOrDefault() {
		return fmt.Errorf("config: cost aqe EXPLAIN ANALYZE requires cost.enabled")
	}
	if !c.Cost.AQE.ReadOnly {
		return fmt.Errorf("config: cost aqe EXPLAIN ANALYZE requires readOnly: true")
	}
	if c.Cost.AQE.Timeout == 0 {
		return fmt.Errorf("config: cost aqe timeout must be greater than 0 when EXPLAIN ANALYZE is enabled")
	}
	return nil
}

func (c *Config) validateBudget() error {
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
	return nil
}

func (c *Config) validateEntities(databases map[string]DatabaseConfig) error {
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
		if err := c.validateEntity(e, entitySources, entityConfigs); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) validateEntity(
	e EntityConfig,
	entitySources map[string]string,
	entityConfigs map[string]EntityConfig,
) error {
	if duplicate, ok := firstDuplicateField(e.Fields); ok {
		return fmt.Errorf("config: entity %q has duplicate field %q", e.Name, duplicate)
	}
	if duplicate, ok := firstDuplicate(e.Params); ok {
		return fmt.Errorf("config: entity %q has duplicate parameter %q", e.Name, duplicate)
	}
	if e.MCP.TrustedProcedure && e.Kind != "procedure" {
		return fmt.Errorf("config: entity %q sets trustedProcedure but is not a procedure", e.Name)
	}
	if err := validateEntityFieldACL(e); err != nil {
		return err
	}
	for role, policy := range e.RowPolicies {
		if err := validateRowPolicy(policy); err != nil {
			return fmt.Errorf("config: entity %q row policy for role %q: %w", e.Name, role, err)
		}
	}
	return validateEntityRelationships(e, entitySources, entityConfigs)
}

func validateEntityFieldACL(e EntityConfig) error {
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
			return fmt.Errorf(
				"config: entity %q field ACL for role %q has duplicate read field %q",
				e.Name,
				role,
				duplicate,
			)
		}
		if duplicate, ok := firstDuplicate(acl.Write); ok {
			return fmt.Errorf(
				"config: entity %q field ACL for role %q has duplicate write field %q",
				e.Name,
				role,
				duplicate,
			)
		}
		for _, field := range append(append([]string{}, acl.Read...), acl.Write...) {
			if !visibleFields[field] {
				return fmt.Errorf(
					"config: entity %q field ACL for role %q references unknown or excluded field %q",
					e.Name,
					role,
					field,
				)
			}
		}
	}
	return nil
}

func validateEntityRelationships(
	e EntityConfig,
	entitySources map[string]string,
	entityConfigs map[string]EntityConfig,
) error {
	relationNames := make(map[string]bool, len(e.Relationships))
	localFields := configuredFields(e.Fields)
	for _, relation := range e.Relationships {
		if err := validateRelationship(e, relation, relationNames, localFields, entitySources, entityConfigs); err != nil {
			return err
		}
	}
	return nil
}

func validateRelationship(
	e EntityConfig,
	relation RelationshipConfig,
	relationNames map[string]bool,
	localFields map[string]bool,
	entitySources map[string]string,
	entityConfigs map[string]EntityConfig,
) error {
	if err := validateRelationshipIdentity(e, relation, relationNames, entitySources); err != nil {
		return err
	}
	if err := validateRelationshipScope(e, relation, entitySources); err != nil {
		return err
	}
	return validateRelationshipJoin(relation, localFields, entityConfigs)
}

func validateRelationshipIdentity(
	e EntityConfig,
	relation RelationshipConfig,
	relationNames map[string]bool,
	entitySources map[string]string,
) error {
	if relation.Name == "" {
		return fmt.Errorf("config: entity %q has relationship with empty name", e.Name)
	}
	if relationNames[relation.Name] {
		return fmt.Errorf("config: entity %q has duplicate relationship %q", e.Name, relation.Name)
	}
	relationNames[relation.Name] = true
	if _, ok := entitySources[relation.Target]; !ok {
		return fmt.Errorf(
			"config: entity %q relationship %q references unknown target %q",
			e.Name,
			relation.Name,
			relation.Target,
		)
	}
	return nil
}

func validateRelationshipScope(
	e EntityConfig,
	relation RelationshipConfig,
	entitySources map[string]string,
) error {
	targetSource := entitySources[relation.Target]
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
		return fmt.Errorf(
			"config: relationship %q has invalid cardinality %q",
			relation.Name,
			relation.Cardinality,
		)
	}
	return nil
}

func validateRelationshipJoin(
	relation RelationshipConfig,
	localFields map[string]bool,
	entityConfigs map[string]EntityConfig,
) error {
	if len(relation.JoinOn) != 1 {
		return fmt.Errorf("config: relationship %q requires exactly one joinOn pair", relation.Name)
	}
	targetFields := configuredFields(entityConfigs[relation.Target].Fields)
	for local, target := range relation.JoinOn {
		if !localFields[local] {
			return fmt.Errorf("config: relationship %q references unknown local field %q", relation.Name, local)
		}
		if !targetFields[target] {
			return fmt.Errorf(
				"config: relationship %q references unknown target field %q",
				relation.Name,
				target,
			)
		}
	}
	return nil
}

func (c *Config) normalizeRoles() error {
	c.Server.Role = canonicalRole(c.Server.Role)
	for i := range c.Entities {
		if err := c.normalizeEntityRoles(&c.Entities[i]); err != nil {
			return err
		}
	}
	return c.normalizeBudgetRoles()
}

func (c *Config) normalizeEntityRoles(e *EntityConfig) error {
	e.Roles.Read = canonicalRoleList(e.Roles.Read)
	e.Roles.Create = canonicalRoleList(e.Roles.Create)
	e.Roles.Update = canonicalRoleList(e.Roles.Update)
	e.Roles.Delete = canonicalRoleList(e.Roles.Delete)
	e.Roles.Execute = canonicalRoleList(e.Roles.Execute)
	e.Roles.Aggregate = canonicalRoleList(e.Roles.Aggregate)

	fieldACL := make(map[string]FieldACLConfig, len(e.FieldACL))
	for role, acl := range e.FieldACL {
		key := canonicalRole(role)
		if key == "" {
			return fmt.Errorf("config: entity %q has an empty field ACL role", e.Name)
		}
		if _, exists := fieldACL[key]; exists {
			return fmt.Errorf("config: entity %q has colliding field ACL role %q after normalization", e.Name, key)
		}
		fieldACL[key] = acl
	}
	e.FieldACL = fieldACL

	policies := make(RowPolicies, len(e.RowPolicies))
	for role, policy := range e.RowPolicies {
		key := canonicalRole(role)
		if key == "" {
			return fmt.Errorf("config: entity %q has an empty row-policy role", e.Name)
		}
		if _, exists := policies[key]; exists {
			return fmt.Errorf("config: entity %q has colliding row-policy role %q after normalization", e.Name, key)
		}
		policies[key] = policy
	}
	e.RowPolicies = policies
	return nil
}

func (c *Config) normalizeBudgetRoles() error {
	roles := make(map[string]BudgetLimits, len(c.Budget.Roles))
	for role, limits := range c.Budget.Roles {
		key := canonicalRole(role)
		if key == "" {
			return fmt.Errorf("config: budget has an empty role")
		}
		if _, exists := roles[key]; exists {
			return fmt.Errorf("config: budget has colliding role %q after normalization", key)
		}
		roles[key] = limits
	}
	c.Budget.Roles = roles
	return nil
}

func validDriverName(name string) bool {
	for i, r := range name {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if i > 0 && ((r >= '0' && r <= '9') || r == '-' || r == '_') {
			continue
		}
		return false
	}
	return name != ""
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
	if limits.MaxConcurrent < 0 || limits.MaxExecution < 0 || limits.MaxEstimatedScannedRows < 0 ||
		limits.MaxScannedRows < 0 || limits.MaxReturnedRows < 0 ||
		limits.MaxReturnedBytes < 0 || limits.MaxSessionCost < 0 {
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
